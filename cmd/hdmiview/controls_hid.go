//go:build !linux

package main

import (
	"encoding/binary"
	"fmt"

	"github.com/jkandasa/hdmiview/ms2109"
)

// Picture controls written straight to the MS2109 over HID, for systems
// whose capture API (via ffmpeg) cannot set them. FE90.. holds the hardware
// registers (brightness s8, contrast, saturation, hue s8); C6A0.. holds the
// same values as the UVC layer reports them (int16 LE: brightness,
// contrast, hue, saturation), so both are updated together.

const (
	regPicture = 0xFE90
	regUVC     = 0xC6A0
)

var hidControls = []struct {
	id       uint32
	name     string
	min, max int32
	def      int32 // mask ROM default
	reg, uvc int   // offsets into regPicture and regUVC
	signed   bool
}{
	{0x00980900, "Brightness", -128, 127, -11, 0, 0, true},
	{0x00980901, "Contrast", 0, 255, 148, 1, 2, false},
	{0x00980902, "Saturation", 0, 255, 180, 2, 6, false},
	{0x00980903, "Hue", -128, 127, 0, 3, 4, true},
}

func openChip() (*ms2109.Device, error) {
	d, err := ms2109.Open("")
	if err != nil {
		return nil, fmt.Errorf("picture controls need the MS2109 HID interface: %w", err)
	}
	if id, err := d.ChipID(); err != nil || id != ms2109.ChipIDMS2109 {
		d.Close()
		return nil, fmt.Errorf("picture controls: not an MS2109 (chip id 0x%02x, %v)", id, err)
	}
	return d, nil
}

func (b *ffmpegBackend) listControls(string) ([]videoControl, error) {
	d, err := openChip()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	regs, err := d.XDATARead(regPicture, 4)
	if err != nil {
		return nil, err
	}
	out := make([]videoControl, 0, len(hidControls))
	for _, c := range hidControls {
		v := int32(regs[c.reg])
		if c.signed {
			v = int32(int8(regs[c.reg]))
		}
		out = append(out, videoControl{ID: c.id, Name: c.name, Type: "int", Min: c.min, Max: c.max, Step: 1, Default: c.def, Value: v})
	}
	return out, nil
}

func (b *ffmpegBackend) setControl(_ string, id uint32, value int32) (int32, error) {
	for _, c := range hidControls {
		if c.id != id {
			continue
		}
		d, err := openChip()
		if err != nil {
			return 0, err
		}
		defer d.Close()
		if err := d.XDATAWrite(regPicture+c.reg, []byte{byte(value)}); err != nil {
			return 0, err
		}
		uvc := binary.LittleEndian.AppendUint16(nil, uint16(int16(value)))
		if err := d.XDATAWrite(regUVC+c.uvc, uvc); err != nil {
			return 0, err
		}
		got, err := d.XDATARead(regPicture+c.reg, 1)
		if err != nil {
			return 0, err
		}
		if c.signed {
			return int32(int8(got[0])), nil
		}
		return int32(got[0]), nil
	}
	return 0, fmt.Errorf("unknown control 0x%x", id)
}
