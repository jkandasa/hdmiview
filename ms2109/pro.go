package ms2109

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// Pro-style patch for the stock MS2109 EEPROM firmware (ported from the
// behaviour of github.com/matiaspl/MS2109-Pro, applied to the user's own
// stock image instead of the upstream baseline).
//
// The mask ROM calls the EEPROM main hook at 0xCC00 with R7 = boot/event
// stage (ROM dispatcher 0x6EF8 -> 0x6FAC). Stage 1 follows ROM hardware init
// (0x4D54), stage 2 follows 0x5765; both run before streaming starts.

type ProOptions struct {
	FrameRates   bool   // restore datasheet MJPEG rates (no fake 60 fps above 720p)
	Procamp      bool   // startup brightness/contrast/saturation/hue
	Stereo48k    bool   // UAC descriptor: 2 channels, 48 kHz
	ChannelOrder bool   // FE60=1 L/R routing fix from MS2109-Pro
	BT709        bool   // UVC colour matching: BT.709 matrix
	Name         string // video/audio interface name in EEPROM header, "" = keep

	EDID          []byte // replacement EDID (128 or 256 bytes), nil = keep
	EDIDPreferred string // name in StandardTimings to make the preferred mode, "" = keep
}

func DefaultProOptions() ProOptions {
	// ChannelOrder is off: MS2109-Pro's FE60=1 routing swapped L/R on this device.
	return ProOptions{FrameRates: true, Procamp: true, Stereo48k: true, ChannelOrder: false, BT709: true, Name: "MS2109-Pro"}
}

var (
	// First bytes of stock code: MOV DPTR,#DE0B; MOV A,R7; MOVX @DPTR,A
	stockHookEntry = []byte{0x90, 0xDE, 0x0B, 0xEF, 0xF0}

	// MJPEG frame timing table at XDATA 0xCE52, copied by the stock code into
	// the first five MJPEG frame descriptors (25 bytes) and the default interval
	// of all eleven: dwDefault, bNumIntervals, 5 x dwFrameInterval (100 ns, LE).
	frameTableAddr = 0xCE52
	frameTableOld  = intervals(166666, 5, 166666, 333333, 400000, 500000, 1000000)
	// Datasheet table 6.3: 1080p..1280x960 MJPEG = 30/25/20/10/5 fps.
	frameTableNew = intervals(333333, 5, 333333, 400000, 500000, 1000000, 2000000)

	colorMatchingRecords = []int{0xC2A4, 0xC43B}
	colorMatchingStock   = []byte{0x06, 0x24, 0x0D, 0x01, 0x01, 0x04}
)

func intervals(def uint32, n byte, iv ...uint32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, def)
	b = append(b, n)
	for _, v := range iv {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// ProChange describes one modification, for reporting.
type ProChange struct {
	What   string
	Offset int // EEPROM offset
	Len    int
}

// BuildPro applies the patch to a stock image and returns the new image.
func BuildPro(stock []byte, opt ProOptions) ([]byte, []ProChange, error) {
	info, err := ParseImage(stock)
	if err != nil {
		return nil, nil, err
	}
	if !info.ChecksumsOK() {
		return nil, nil, fmt.Errorf("stock image checksums invalid")
	}
	if info.Wide {
		return nil, nil, fmt.Errorf("16-bit EEPROM layout not supported")
	}
	if info.Hooks&1 == 0 {
		return nil, nil, fmt.Errorf("main hook (bit0) not enabled in stock image")
	}
	if !bytes.Equal(stock[HeaderLen:HeaderLen+len(stockHookEntry)], stockHookEntry) {
		return nil, nil, fmt.Errorf("unexpected hook entry %x (already patched?)", stock[HeaderLen:HeaderLen+5])
	}

	img := bytes.Clone(stock)
	var changes []ProChange
	codeOff := func(xaddr int) int { return HeaderLen + xaddr - CodeLoadAddr }

	if opt.FrameRates {
		off := codeOff(frameTableAddr)
		if !bytes.Equal(img[off:off+len(frameTableOld)], frameTableOld) {
			return nil, nil, fmt.Errorf("MJPEG frame table at 0x%04X does not match stock", frameTableAddr)
		}
		copy(img[off:], frameTableNew)
		changes = append(changes, ProChange{"MJPEG frame table: 1080p..1280x960 -> 30/25/20/10/5 fps, default 30 fps", off, len(frameTableNew)})
	}

	if opt.Name != "" {
		if len(opt.Name) > 15 {
			return nil, nil, fmt.Errorf("name longer than 15 bytes")
		}
		for _, c := range []byte(opt.Name) {
			if c < 0x20 || c > 0x7E {
				return nil, nil, fmt.Errorf("name must be printable ASCII")
			}
		}
		// Record: total length (1 + text), text, 0xFF padding to 16 bytes.
		rec := append([]byte{byte(len(opt.Name) + 1)}, opt.Name...)
		rec = append(rec, bytes.Repeat([]byte{0xFF}, 16-len(rec))...)
		copy(img[0x10:], rec) // video interface name
		copy(img[0x20:], rec) // audio interface name
		changes = append(changes, ProChange{fmt.Sprintf("header video/audio name %q", opt.Name), 0x10, 32})
	}

	if opt.EDID != nil || opt.EDIDPreferred != "" {
		cur, err := ImageEDID(img)
		if err != nil {
			return nil, nil, err
		}
		e := bytes.Clone(cur)
		var what []string
		if opt.EDID != nil {
			if err := ValidateEDID(opt.EDID); err != nil {
				return nil, nil, fmt.Errorf("--edid: %w", err)
			}
			clear(e)
			copy(e, opt.EDID)
			what = append(what, fmt.Sprintf("replaced (%d bytes)", len(opt.EDID)))
			if opt.EDID[126] == 0 {
				what = append(what, "WARNING: no CEA-861 extension, HDMI sources may switch to DVI mode without audio")
			}
		}
		if opt.EDIDPreferred != "" {
			t, ok := StandardTimings[opt.EDIDPreferred]
			if !ok {
				return nil, nil, fmt.Errorf("unknown preferred mode %q (use one of %s)", opt.EDIDPreferred, strings.Join(StandardTimingNames(), ", "))
			}
			if err := SetPreferredTiming(e, t); err != nil {
				return nil, nil, err
			}
			what = append(what, "preferred mode "+t.String())
		}
		copy(img[EDIDOffset:], e)
		changes = append(changes, ProChange{"EDID: " + strings.Join(what, "; "), EDIDOffset, EDIDLen})
	}

	// Hook code appended after the stock code.
	hookAddr := CodeLoadAddr + info.CodeLen
	a := newAsm(hookAddr)
	a.movAR7()
	a.cjneA(1, "not1")
	if opt.Procamp {
		// UVC PU state: brightness int16 LE (-17), contrast, hue, saturation (int16 LE)
		a.store(0xC6A0, 0xEF, 0xFF, 148, 0, 0, 0, 146, 0)
		// Hardware: brightness s8, contrast, saturation, hue
		a.store(0xFE90, 0xEF, 148, 146, 0)
	}
	a.lcall("desc")
	a.sjmp("done")
	a.label("not1")
	a.cjneA(2, "done") // A still holds R7
	a.lcall("desc")
	a.label("done")
	a.emit(stockHookEntry[:3]...) // displaced MOV DPTR,#DE0B
	a.ret()

	a.label("desc")
	if opt.Stereo48k {
		a.store(0xC4C5, 2)                // bNrChannels
		a.store(0xC4C9, 0x80, 0xBB, 0x00) // tSamFreq = 48000
	}
	if opt.ChannelOrder {
		a.store(0xFE60, 1)
	}
	if opt.BT709 {
		// Only touch a record that still looks exactly like the stock one.
		for i, rec := range colorMatchingRecords {
			skip := fmt.Sprintf("cm%d", i)
			a.movDPTR(rec)
			for j, v := range colorMatchingStock {
				if j > 0 {
					a.incDPTR()
				}
				a.movxLoad()
				a.cjneA(v, skip)
			}
			a.movA(1) // bMatrixCoefficients = BT.709
			a.movxStore()
			a.label(skip)
		}
	}
	a.ret()

	patch, err := a.finish()
	if err != nil {
		return nil, nil, err
	}

	newLen := info.CodeLen + len(patch)
	if HeaderLen+newLen+4 > len(img) {
		return nil, nil, fmt.Errorf("patch does not fit: need %d bytes, EEPROM %d", HeaderLen+newLen+4, len(img))
	}
	end := HeaderLen + newLen
	for i := HeaderLen + info.CodeLen + 4; i < end+4; i++ {
		if img[i] != 0xFF {
			return nil, nil, fmt.Errorf("EEPROM area after stock code not erased at 0x%x", i)
		}
	}
	copy(img[HeaderLen+info.CodeLen:], patch)
	img[HeaderLen] = 0x12 // LCALL hook (replaces MOV DPTR,#DE0B)
	img[HeaderLen+1] = byte(hookAddr >> 8)
	img[HeaderLen+2] = byte(hookAddr)
	binary.BigEndian.PutUint16(img[2:], uint16(newLen))
	changes = append(changes,
		ProChange{fmt.Sprintf("hook entry: LCALL 0x%04X", hookAddr), HeaderLen, 3},
		ProChange{fmt.Sprintf("boot hook code at XDATA 0x%04X (%d bytes)", hookAddr, len(patch)), HeaderLen + info.CodeLen, len(patch)},
		ProChange{fmt.Sprintf("code length 0x%04x -> 0x%04x + checksums", info.CodeLen, newLen), 2, 2},
	)
	if err := FixChecksums(img); err != nil {
		return nil, nil, err
	}
	return img, changes, nil
}
