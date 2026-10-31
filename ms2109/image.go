package ms2109

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

// EEPROM image layout:
//
//	0x00-0x01   magic: A5 5A (24C02..24C16, 8-bit addr) / 96 69 (24C32/64, 16-bit addr)
//	0x02-0x03   code length N (big endian)
//	0x04        hook enable bits (bit0 main-loop hook @0xCC00, bit2 IRQ hook @0xCC20)
//	0x08-0x09   USB PID, 0x0C-0x0F firmware version
//	0x30        code (N bytes), copied by the ROM to XDATA 0xCC00
//	0x30+N      16-bit sum of bytes 0x02..0x2F (big endian)
//	0x32+N      16-bit sum of the code (big endian)
const HeaderLen = 0x30

var (
	Magic8Bit  = [2]byte{0xA5, 0x5A}
	Magic16Bit = [2]byte{0x96, 0x69}
)

type ImageInfo struct {
	Magic         [2]byte
	Wide          bool
	CodeLen       int
	Hooks         byte
	PID           uint16
	Version       []byte
	HdrSumStored  uint16
	HdrSumCalc    uint16
	CodeSumStored uint16
	CodeSumCalc   uint16
	Used          int
}

func (i *ImageInfo) ChecksumsOK() bool {
	return i.HdrSumStored == i.HdrSumCalc && i.CodeSumStored == i.CodeSumCalc
}

func Sum16(b []byte) uint16 {
	var s uint16
	for _, v := range b {
		s += uint16(v)
	}
	return s
}

func ParseImage(img []byte) (*ImageInfo, error) {
	if len(img) < HeaderLen+4 {
		return nil, fmt.Errorf("image too short (%d bytes)", len(img))
	}
	i := &ImageInfo{Magic: [2]byte{img[0], img[1]}}
	switch i.Magic {
	case Magic8Bit:
	case Magic16Bit:
		i.Wide = true
	default:
		return nil, fmt.Errorf("unknown magic %x (blank EEPROM?)", img[:2])
	}
	i.CodeLen = int(binary.BigEndian.Uint16(img[2:]))
	end := HeaderLen + i.CodeLen
	if end+4 > len(img) {
		return nil, fmt.Errorf("code length 0x%x exceeds image size %d", i.CodeLen, len(img))
	}
	i.Hooks = img[4]
	i.PID = binary.BigEndian.Uint16(img[8:])
	i.Version = img[0x0C:0x10]
	i.HdrSumStored = binary.BigEndian.Uint16(img[end:])
	i.CodeSumStored = binary.BigEndian.Uint16(img[end+2:])
	i.HdrSumCalc = Sum16(img[2:HeaderLen])
	i.CodeSumCalc = Sum16(img[HeaderLen:end])
	i.Used = end + 4
	return i, nil
}

// FixChecksums rewrites both checksums in place.
func FixChecksums(img []byte) error {
	i, err := ParseImage(img)
	if err != nil {
		return err
	}
	end := HeaderLen + i.CodeLen
	binary.BigEndian.PutUint16(img[end:], Sum16(img[2:HeaderLen]))
	binary.BigEndian.PutUint16(img[end+2:], Sum16(img[HeaderLen:end]))
	return nil
}

func PrintInfo(w io.Writer, label string, img []byte) bool {
	i, err := ParseImage(img)
	if err != nil {
		fmt.Fprintf(w, "%s: invalid image: %v\n", label, err)
		return false
	}
	ok := func(a, b uint16) string {
		if a == b {
			return "OK"
		}
		return "MISMATCH"
	}
	mode := "8-bit"
	if i.Wide {
		mode = "16-bit"
	}
	sum := sha256.Sum256(img)
	fmt.Fprintf(w, "%s\n", label)
	fmt.Fprintf(w, "  size        : %d bytes, used %d\n", len(img), i.Used)
	fmt.Fprintf(w, "  magic       : %x (%s I2C addressing)\n", i.Magic, mode)
	fmt.Fprintf(w, "  code length : 0x%04x (%d) -> XDATA 0x%04X\n", i.CodeLen, i.CodeLen, CodeLoadAddr)
	fmt.Fprintf(w, "  hook flags  : 0x%02x\n", i.Hooks)
	fmt.Fprintf(w, "  USB PID     : 0x%04x\n", i.PID)
	fmt.Fprintf(w, "  version     : %x\n", i.Version)
	fmt.Fprintf(w, "  hdr  sum    : stored 0x%04x calc 0x%04x %s\n", i.HdrSumStored, i.HdrSumCalc, ok(i.HdrSumStored, i.HdrSumCalc))
	fmt.Fprintf(w, "  code sum    : stored 0x%04x calc 0x%04x %s\n", i.CodeSumStored, i.CodeSumCalc, ok(i.CodeSumStored, i.CodeSumCalc))
	fmt.Fprintf(w, "  sha256      : %s\n", hex.EncodeToString(sum[:]))
	return i.ChecksumsOK()
}

func Hexdump(w io.Writer, data []byte, base int) {
	for off := 0; off < len(data); off += 16 {
		chunk := data[off:min(off+16, len(data))]
		asc := make([]byte, len(chunk))
		for j, c := range chunk {
			if c >= 32 && c < 127 {
				asc[j] = c
			} else {
				asc[j] = '.'
			}
		}
		fmt.Fprintf(w, "%04x: % -47x  %s\n", base+off, chunk, asc)
	}
}
