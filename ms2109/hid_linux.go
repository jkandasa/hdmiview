package ms2109

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Linux: /dev/hidrawN with the HIDIOCSFEATURE / HIDIOCGFEATURE ioctls.

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

type hidraw struct{ f *os.File }

func openTransport(path string) (hidTransport, string, error) {
	if path == "" {
		var err error
		if path, err = FindHidraw(); err != nil {
			return nil, "", err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, "", fmt.Errorf("%w (install udev/70-ms2109.rules or run with sudo)", err)
		}
		return nil, "", err
	}
	return &hidraw{f}, path, nil
}

func (h *hidraw) ioctl(req uintptr, buf []byte) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, h.f.Fd(), req, uintptr(unsafe.Pointer(&buf[0])))
	if e != 0 {
		return e
	}
	return nil
}

func (h *hidraw) setFeature(buf []byte) error { return h.ioctl(hidiocSFeature, buf) }
func (h *hidraw) getFeature(buf []byte) error { return h.ioctl(hidiocGFeature, buf) }
func (h *hidraw) close() error                { return h.f.Close() }
