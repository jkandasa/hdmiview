//go:build !linux && !windows && !darwin

package ms2109

import (
	"errors"
	"runtime"
)

func openTransport(string) (hidTransport, string, error) {
	return nil, "", errors.New("HID access is not implemented on " + runtime.GOOS)
}
