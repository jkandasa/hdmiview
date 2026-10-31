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
	"fmt"
	"os"
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

// hidTransport moves raw feature reports. buf always holds the report id
// (0) followed by 8 data bytes. Implemented per OS in hid_<os>.go.
type hidTransport interface {
	setFeature(buf []byte) error
	getFeature(buf []byte) error
	close() error
}

type Device struct {
	t       hidTransport
	Path    string
	Verbose bool
}

// Open opens the MS2109 HID interface. path selects a specific device
// (OS specific, e.g. /dev/hidraw5 on Linux); "" finds the first MS2109.
func Open(path string) (*Device, error) {
	t, resolved, err := openTransport(path)
	if err != nil {
		return nil, err
	}
	return &Device{t: t, Path: resolved}, nil
}

func (d *Device) Close() error { return d.t.close() }

// Exchange sends one feature report and reads the reply, checking that the
// first checkLen bytes are echoed back.
func (d *Device) Exchange(out [reportLen]byte, checkLen int) ([reportLen]byte, error) {
	var in [reportLen]byte
	if d.Verbose {
		fmt.Fprintln(os.Stderr, "OUT", hex.EncodeToString(out[:]))
	}
	if err := d.t.setFeature(out[:]); err != nil {
		return in, fmt.Errorf("set feature: %w", err)
	}
	if err := d.t.getFeature(in[:]); err != nil {
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
