package ms2109

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// EDID handling. The stock MS2109 firmware carries the 256-byte EDID that the
// HDMI source reads (base block + CEA-861 extension) in its code area at
// XDATA 0xCD52, i.e. EEPROM offset 0x182. The ROM uses it instead of its
// built-in EDID when bit 4 of header byte 5 is set.

const (
	EDIDAddr    = 0xCD52
	EDIDOffset  = HeaderLen + EDIDAddr - CodeLoadAddr // 0x182
	EDIDLen     = 256
	edidEnabled = 0x10 // header[5] bit 4: use the EEPROM EDID
)

var edidHeader = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}

// ValidateEDID checks the header, the block checksums and the extension
// count of a 128- or 256-byte EDID.
func ValidateEDID(e []byte) error {
	if len(e) != 128 && len(e) != 256 {
		return fmt.Errorf("EDID must be 128 or 256 bytes, got %d", len(e))
	}
	if !bytes.Equal(e[:8], edidHeader) {
		return errors.New("not an EDID (bad header)")
	}
	ext := int(e[126])
	if ext > 1 {
		return fmt.Errorf("EDID has %d extension blocks; at most 1 fits", ext)
	}
	if ext == 1 && len(e) < 256 {
		return errors.New("EDID announces an extension block but is only 128 bytes")
	}
	for b := 0; b <= ext; b++ {
		if s := sum8(e[b*128 : (b+1)*128]); s != 0 {
			return fmt.Errorf("EDID block %d checksum is wrong", b)
		}
	}
	return nil
}

func sum8(b []byte) byte {
	var s byte
	for _, v := range b {
		s += v
	}
	return s
}

// fixEDIDChecksum recomputes the checksum byte of one 128-byte block.
func fixEDIDChecksum(block []byte) {
	block[127] = 0
	block[127] = -sum8(block)
}

// Timing is a video timing as stored in an EDID detailed timing descriptor.
type Timing struct {
	PixelClockKHz          int
	HActive, HFront, HSync int
	HBlank                 int
	VActive, VFront, VSync int
	VBlank                 int
}

func (t Timing) RefreshHz() float64 {
	total := (t.HActive + t.HBlank) * (t.VActive + t.VBlank)
	if total == 0 {
		return 0
	}
	return float64(t.PixelClockKHz) * 1000 / float64(total)
}

func (t Timing) String() string {
	return fmt.Sprintf("%dx%d @ %.2f Hz (%.2f MHz)", t.HActive, t.VActive, t.RefreshHz(), float64(t.PixelClockKHz)/1000)
}

// Standard CEA-861 timings usable as the preferred mode.
var StandardTimings = map[string]Timing{
	"1080p60": {148500, 1920, 88, 44, 280, 1080, 4, 5, 45},
	"1080p50": {148500, 1920, 528, 44, 720, 1080, 4, 5, 45},
	"1080p30": {74250, 1920, 88, 44, 280, 1080, 4, 5, 45},
	"1080p25": {74250, 1920, 528, 44, 720, 1080, 4, 5, 45},
	"720p60":  {74250, 1280, 110, 40, 370, 720, 5, 5, 30},
	"720p50":  {74250, 1280, 440, 40, 700, 720, 5, 5, 30},
}

func StandardTimingNames() []string {
	names := make([]string, 0, len(StandardTimings))
	for n := range StandardTimings {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// decodeDTD decodes an 18-byte detailed timing descriptor; ok is false for
// display descriptors (pixel clock 0).
func decodeDTD(d []byte) (Timing, bool) {
	pc := int(d[0]) | int(d[1])<<8
	if pc == 0 {
		return Timing{}, false
	}
	return Timing{
		PixelClockKHz: pc * 10,
		HActive:       int(d[2]) | int(d[4]&0xF0)<<4,
		HBlank:        int(d[3]) | int(d[4]&0x0F)<<8,
		VActive:       int(d[5]) | int(d[7]&0xF0)<<4,
		VBlank:        int(d[6]) | int(d[7]&0x0F)<<8,
		HFront:        int(d[8]) | int(d[11]&0xC0)<<2,
		HSync:         int(d[9]) | int(d[11]&0x30)<<4,
		VFront:        int(d[10]>>4) | int(d[11]&0x0C)<<2,
		VSync:         int(d[10]&0x0F) | int(d[11]&0x03)<<4,
	}, true
}

// encodeDTD writes t into d, keeping image size, borders and flags.
func encodeDTD(d []byte, t Timing) {
	pc := t.PixelClockKHz / 10
	d[0], d[1] = byte(pc), byte(pc>>8)
	d[2], d[3] = byte(t.HActive), byte(t.HBlank)
	d[4] = byte(t.HActive>>8)<<4 | byte(t.HBlank>>8)&0x0F
	d[5], d[6] = byte(t.VActive), byte(t.VBlank)
	d[7] = byte(t.VActive>>8)<<4 | byte(t.VBlank>>8)&0x0F
	d[8], d[9] = byte(t.HFront), byte(t.HSync)
	d[10] = byte(t.VFront&0x0F)<<4 | byte(t.VSync&0x0F)
	d[11] = byte(t.HFront>>8&3)<<6 | byte(t.HSync>>8&3)<<4 | byte(t.VFront>>4&3)<<2 | byte(t.VSync>>4&3)
}

// SetPreferredTiming replaces the first detailed timing descriptor of the
// base block (the preferred mode) and fixes the checksum.
func SetPreferredTiming(e []byte, t Timing) error {
	if err := ValidateEDID(e); err != nil {
		return err
	}
	if _, ok := decodeDTD(e[54:72]); !ok {
		return errors.New("first descriptor is not a detailed timing")
	}
	encodeDTD(e[54:72], t)
	fixEDIDChecksum(e[:128])
	return nil
}

// EDIDInfo is a decoded summary of an EDID.
type EDIDInfo struct {
	Manufacturer string
	ProductCode  int
	Name         string
	Version      string
	Preferred    Timing
	Timings      []Timing // all detailed timings (base + extension)
	VICs         []int    // CEA short video descriptors
	Extensions   int
}

func DecodeEDID(e []byte) (*EDIDInfo, error) {
	if err := ValidateEDID(e); err != nil {
		return nil, err
	}
	m := int(e[8])<<8 | int(e[9])
	info := &EDIDInfo{
		Manufacturer: string([]byte{byte(m>>10&31) + 64, byte(m>>5&31) + 64, byte(m&31) + 64}),
		ProductCode:  int(e[10]) | int(e[11])<<8,
		Version:      fmt.Sprintf("%d.%d", e[18], e[19]),
		Extensions:   int(e[126]),
	}
	for i := 0; i < 4; i++ {
		d := e[54+18*i : 72+18*i]
		if t, ok := decodeDTD(d); ok {
			if len(info.Timings) == 0 {
				info.Preferred = t
			}
			info.Timings = append(info.Timings, t)
		} else if d[3] == 0xFC {
			info.Name = strings.TrimRight(strings.SplitN(string(d[5:18]), "\n", 2)[0], " ")
		}
	}
	if info.Extensions == 1 && e[128] == 0x02 { // CEA-861 extension
		x := e[128:256]
		dtdStart := int(x[2])
		for p := 4; p < dtdStart && p < 127; {
			tag, n := int(x[p]>>5), int(x[p]&0x1F)
			if tag == 2 { // video data block
				for _, v := range x[p+1 : p+1+n] {
					info.VICs = append(info.VICs, int(v&0x7F))
				}
			}
			p += 1 + n
		}
		for p := dtdStart; p >= 4 && p+18 <= 127; p += 18 {
			if t, ok := decodeDTD(x[p : p+18]); ok {
				info.Timings = append(info.Timings, t)
			}
		}
	}
	return info, nil
}

func PrintEDID(w io.Writer, label string, e []byte) error {
	info, err := DecodeEDID(e)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	fmt.Fprintf(w, "%s\n", label)
	fmt.Fprintf(w, "  monitor name : %s\n", info.Name)
	fmt.Fprintf(w, "  manufacturer : %s, product 0x%04x, EDID %s, %d extension(s)\n", info.Manufacturer, info.ProductCode, info.Version, info.Extensions)
	fmt.Fprintf(w, "  preferred    : %s\n", info.Preferred)
	for i, t := range info.Timings[min(1, len(info.Timings)):] {
		fmt.Fprintf(w, "  timing %d     : %s\n", i+2, t)
	}
	if len(info.VICs) > 0 {
		fmt.Fprintf(w, "  CEA VICs     : %v\n", info.VICs)
	}
	return nil
}

// ImageEDID returns the EDID embedded in a firmware image.
func ImageEDID(img []byte) ([]byte, error) {
	i, err := ParseImage(img)
	if err != nil {
		return nil, err
	}
	if HeaderLen+i.CodeLen < EDIDOffset+EDIDLen {
		return nil, errors.New("firmware code is too short to contain an EDID")
	}
	e := img[EDIDOffset : EDIDOffset+EDIDLen]
	if err := ValidateEDID(e); err != nil {
		return nil, fmt.Errorf("no valid EDID at EEPROM offset 0x%x: %w", EDIDOffset, err)
	}
	if img[5]&edidEnabled == 0 {
		return nil, errors.New("the EEPROM EDID is not enabled in this image (header byte 5 bit 4)")
	}
	return e, nil
}
