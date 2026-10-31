//go:build darwin && !cgo

package ms2109

import "errors"

// The macOS HID transport needs cgo (IOKit). Release builds always enable
// it; this stub only keeps CGO_ENABLED=0 builds compiling.
func openTransport(string) (hidTransport, string, error) {
	return nil, "", errors.New("HID access on macOS needs a build with cgo enabled")
}
