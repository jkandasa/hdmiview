// Package ms2109 talks to the factory HID interface of the MacroSilicon
// MS2109 USB HDMI capture chip (USB 534d:2109).
//
// Protocol (mask ROM, see github.com/BertoldVdb/ms-tools): 9 byte HID feature
// report with report id 0:
//
//	[0x00, cmd, addr_hi, addr_lo, d0, d1, d2, d3, d4]
//
//	0xB5 read XDATA   -> reply in d0
//	0xB6 write XDATA  -> d0 = value
//	0xE5 read EEPROM  -> reply in d0..d4; request d4=1 selects 16-bit I2C addressing
//	0xE6 write EEPROM -> d0 = value (one byte per request, ROM waits for write cycle)
//
// The reply echoes the request header, which is checked.
package ms2109

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	VID = 0x534D
	PID = 0x2109

	ChipIDAddr     = 0xF800
	ChipIDMS2109   = 0xA7
	UserConfigAddr = 0xCBD0
	CodeLoadAddr   = 0xCC00

	cmdXDATARead   = 0xB5
	cmdXDATAWrite  = 0xB6
	cmdEEPROMRead  = 0xE5
	cmdEEPROMWrite = 0xE6

	reportLen = 9
)

func ioc(dir, typ, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | typ<<8 | nr
}

var (
	hidiocSFeature = ioc(3, 'H', 0x06, reportLen)
	hidiocGFeature = ioc(3, 'H', 0x07, reportLen)
)

// FindHidraw returns the /dev/hidrawN node of the first MS2109 found.
func FindHidraw() (string, error) {
	want := fmt.Sprintf("HID_ID=0003:%08X:%08X", VID, PID)
	nodes, _ := filepath.Glob("/sys/class/hidraw/hidraw*")
	for _, n := range nodes {
		ue, err := os.ReadFile(filepath.Join(n, "device/uevent"))
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToUpper(string(ue)), want) {
			return "/dev/" + filepath.Base(n), nil
		}
	}
	return "", errors.New("MS2109 (534d:2109) not found")
}

type Device struct {
	f       *os.File
	Path    string
	Verbose bool
}

func Open(path string) (*Device, error) {
	if path == "" {
		var err error
		if path, err = FindHidraw(); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%w (install udev/70-ms2109.rules or run with sudo)", err)
		}
		return nil, err
	}
	return &Device{f: f, Path: path}, nil
}

func (d *Device) Close() error { return d.f.Close() }

func (d *Device) ioctl(req uintptr, buf []byte) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), req, uintptr(unsafe.Pointer(&buf[0])))
	if e != 0 {
		return e
	}
	return nil
}

// Exchange sends one feature report and reads the reply, checking that the
// first checkLen bytes are echoed back.
func (d *Device) Exchange(out [reportLen]byte, checkLen int) ([reportLen]byte, error) {
	var in [reportLen]byte
	if d.Verbose {
		fmt.Fprintln(os.Stderr, "OUT", hex.EncodeToString(out[:]))
	}
	if err := d.ioctl(hidiocSFeature, out[:]); err != nil {
		return in, fmt.Errorf("set feature: %w", err)
	}
	if err := d.ioctl(hidiocGFeature, in[:]); err != nil {
		return in, fmt.Errorf("get feature: %w", err)
	}
	if d.Verbose {
		fmt.Fprintln(os.Stderr, "IN ", hex.EncodeToString(in[:]))
	}
	if !bytes.Equal(out[:checkLen], in[:checkLen]) {
		return in, fmt.Errorf("bad reply: sent %x got %x", out, in)
	}
	return in, nil
}

func (d *Device) XDATARead(addr, n int) ([]byte, error) {
	buf := make([]byte, 0, n)
	for a := addr; a < addr+n; a++ {
		in, err := d.Exchange([reportLen]byte{0, cmdXDATARead, byte(a >> 8), byte(a)}, 4)
		if err != nil {
			return buf, err
		}
		buf = append(buf, in[4])
	}
	return buf, nil
}

func (d *Device) XDATAWrite(addr int, data []byte) error {
	for i, b := range data {
		a := addr + i
		if _, err := d.Exchange([reportLen]byte{0, cmdXDATAWrite, byte(a >> 8), byte(a), b}, 4); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) ChipID() (byte, error) {
	b, err := d.XDATARead(ChipIDAddr, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// EEPROMRead reads n bytes starting at addr, 5 bytes per request.
// progress, if non-nil, is called with the number of bytes read so far.
func (d *Device) EEPROMRead(addr, n int, wide bool, progress func(int)) ([]byte, error) {
	var flag byte
	if wide {
		flag = 1
	}
	buf := make([]byte, 0, n)
	for a := addr; len(buf) < n; {
		in, err := d.Exchange([reportLen]byte{0, cmdEEPROMRead, byte(a >> 8), byte(a), 0, 0, 0, 0, flag}, 4)
		if err != nil {
			return buf, err
		}
		take := min(5, n-len(buf))
		buf = append(buf, in[4:4+take]...)
		a += take
		if progress != nil {
			progress(len(buf))
		}
	}
	return buf, nil
}

// EEPROMWriteByte writes one byte. The MS2109 ROM waits for the EEPROM write
// cycle itself before answering.
func (d *Device) EEPROMWriteByte(addr int, v byte) error {
	_, err := d.Exchange([reportLen]byte{0, cmdEEPROMWrite, byte(addr >> 8), byte(addr), v}, 4)
	return err
}
