package main

import (
	"bytes"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Platform-independent helpers for the ffmpeg capture backend (used on
// Windows and macOS, unit-tested everywhere).

// ---------------------------------------------------------------- MJPEG stream

var jpegSOI = []byte{0xFF, 0xD8}

// jpegFrameLen returns the length of the complete JPEG at the start of b,
// 0 if more data is needed, or -1 if b does not start with a valid JPEG.
// It walks the marker segments (whose payload may contain any bytes) and
// scans entropy-coded data, where 0xFF is always followed by 0x00 or a
// restart marker, so the first real marker after it is unambiguous.
func jpegFrameLen(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	if b[0] != 0xFF || b[1] != 0xD8 {
		return -1
	}
	i := 2
	for {
		if i+1 >= len(b) {
			return 0
		}
		if b[i] != 0xFF {
			return -1
		}
		m := b[i+1]
		switch {
		case m == 0xFF: // fill byte before a marker
			i++
			continue
		case m == 0xD9: // EOI
			return i + 2
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // markers without length
			i += 2
			continue
		}
		if i+3 >= len(b) {
			return 0
		}
		n := int(b[i+2])<<8 | int(b[i+3])
		if n < 2 {
			return -1
		}
		i += 2 + n
		if m != 0xDA { // not start of scan
			continue
		}
		for { // entropy-coded data up to the next marker
			if i+1 >= len(b) {
				return 0
			}
			if b[i] != 0xFF {
				i++
				continue
			}
			next := b[i+1]
			if next == 0x00 || (next >= 0xD0 && next <= 0xD7) {
				i += 2
				continue
			}
			if next == 0xFF {
				i++
				continue
			}
			break // a marker: handled by the outer loop
		}
	}
}

// mjpegReader splits a concatenated MJPEG byte stream into JPEG frames.
type mjpegReader struct {
	r   io.Reader
	buf []byte
	tmp []byte
}

func newMJPEGReader(r io.Reader) *mjpegReader {
	return &mjpegReader{r: r, tmp: make([]byte, 256<<10)}
}

func (m *mjpegReader) next() ([]byte, error) {
	for {
		switch n := jpegFrameLen(m.buf); {
		case n > 0:
			frame := bytes.Clone(m.buf[:n])
			m.buf = m.buf[:copy(m.buf, m.buf[n:])]
			return frame, nil
		case n < 0: // resynchronise on the next start-of-image marker
			if i := bytes.Index(m.buf[1:], jpegSOI); i >= 0 {
				m.buf = m.buf[:copy(m.buf, m.buf[i+1:])]
			} else if last := len(m.buf) - 1; m.buf[last] == 0xFF {
				m.buf = append(m.buf[:0], 0xFF) // may be the first half of SOI
			} else {
				m.buf = m.buf[:0]
			}
			continue
		}
		k, err := m.r.Read(m.tmp)
		m.buf = append(m.buf, m.tmp[:k]...)
		if err != nil {
			if errors.Is(err, io.EOF) && k > 0 {
				continue
			}
			return nil, err
		}
	}
}

// ---------------------------------------------------------------- devices

type ffDevice struct {
	Name string // friendly name
	Alt  string // DirectShow alternative (unique) name, or AVFoundation index
	Kind string // "video" or "audio"
}

var (
	reQuoted   = regexp.MustCompile(`"([^"]+)"(?:\s+\((video|audio|none)\))?`)
	reAltName  = regexp.MustCompile(`Alternative name\s+"([^"]+)"`)
	reAVFEntry = regexp.MustCompile(`\]\s+\[(\d+)\]\s+(.+?)\s*$`)
)

// parseDshowDevices parses "ffmpeg -list_devices true -f dshow -i dummy".
// Handles both the current format ("name" (video)) and the older one with
// "DirectShow video devices" / "DirectShow audio devices" sections.
func parseDshowDevices(out string) []ffDevice {
	var devs []ffDevice
	section := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "DirectShow video devices"):
			section = "video"
			continue
		case strings.Contains(line, "DirectShow audio devices"):
			section = "audio"
			continue
		}
		if m := reAltName.FindStringSubmatch(line); m != nil {
			if len(devs) > 0 {
				devs[len(devs)-1].Alt = m[1]
			}
			continue
		}
		m := reQuoted.FindStringSubmatch(line)
		if m == nil || !strings.HasPrefix(strings.TrimSpace(line[strings.Index(line, "]")+1:]), `"`) {
			continue
		}
		kind := m[2]
		if kind == "" {
			kind = section
		}
		if kind == "video" || kind == "audio" {
			devs = append(devs, ffDevice{Name: m[1], Kind: kind})
		}
	}
	return devs
}

// parseAVFDevices parses "ffmpeg -f avfoundation -list_devices true -i ”".
func parseAVFDevices(out string) []ffDevice {
	var devs []ffDevice
	section := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "AVFoundation video devices"):
			section = "video"
			continue
		case strings.Contains(line, "AVFoundation audio devices"):
			section = "audio"
			continue
		}
		if m := reAVFEntry.FindStringSubmatch(line); m != nil && section != "" {
			devs = append(devs, ffDevice{Name: m[2], Alt: m[1], Kind: section})
		}
	}
	return devs
}

// pickDevice chooses a device of the given kind. An explicit choice must
// match a name (case-insensitive substring) or alternative name. Otherwise
// a DirectShow alternative name carrying the MS2109 USB ID wins, then the
// first device whose name contains one of the candidates (in order).
func pickDevice(devs []ffDevice, kind, explicit string, candidates []string) (ffDevice, error) {
	var ofKind []ffDevice
	for _, d := range devs {
		if d.Kind == kind {
			ofKind = append(ofKind, d)
		}
	}
	contains := func(s, sub string) bool {
		return sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub))
	}
	if explicit != "" {
		for _, d := range ofKind {
			if d.Name == explicit || d.Alt == explicit {
				return d, nil
			}
		}
		for _, d := range ofKind {
			if contains(d.Name, explicit) {
				return d, nil
			}
		}
		return ffDevice{}, errors.New("no " + kind + " device matching " + strconv.Quote(explicit))
	}
	for _, d := range ofKind {
		if contains(d.Alt, "vid_534d&pid_2109") {
			return d, nil
		}
	}
	for _, c := range candidates {
		for _, d := range ofKind {
			if contains(d.Name, c) {
				return d, nil
			}
		}
	}
	names := make([]string, len(ofKind))
	for i, d := range ofKind {
		names[i] = strconv.Quote(d.Name)
	}
	return ffDevice{}, errors.New("no MS2109 " + kind + " device found (available: " + strings.Join(names, ", ") + ")")
}

// ---------------------------------------------------------------- modes

// Frame rates the MS2109 offers; used to expand a min..max range.
var commonFPS = []int{60, 50, 30, 25, 20, 10, 5}

var (
	reDshowOption = regexp.MustCompile(`(vcodec|pixel_format)=(\S+)\s+min s=(\d+)x(\d+) fps=([\d.]+)\s+max s=(\d+)x(\d+) fps=([\d.]+)`)
	reAVFMode     = regexp.MustCompile(`(\d+)x(\d+)@\[([\d.]+)\s+([\d.]+)\]fps`)
)

func fpsRange(minFPS, maxFPS float64) []int {
	set := map[int]bool{int(math.Round(minFPS)): true, int(math.Round(maxFPS)): true}
	for _, f := range commonFPS {
		if float64(f) >= minFPS-0.01 && float64(f) <= maxFPS+0.01 {
			set[f] = true
		}
	}
	var out []int
	for f := range set {
		if f > 0 {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	slices.Reverse(out)
	return out
}

// addMode merges a format/size/fps entry into modes, keeping first-seen order.
func addMode(modes []videoModeInfo, format string, w, h int, fps []int) []videoModeInfo {
	for i, m := range modes {
		if m.Format == format && m.Width == w && m.Height == h {
			for _, f := range fps {
				if !slices.Contains(m.FPS, f) {
					modes[i].FPS = append(modes[i].FPS, f)
				}
			}
			slices.Sort(modes[i].FPS)
			slices.Reverse(modes[i].FPS)
			return modes
		}
	}
	return append(modes, videoModeInfo{Format: format, Width: w, Height: h, FPS: fps})
}

// parseDshowOptions parses "ffmpeg -f dshow -list_options true -i video=...".
// vcodec=mjpeg becomes MJPG, pixel_format=yuyv422 becomes YUYV.
func parseDshowOptions(out string) []videoModeInfo {
	var modes []videoModeInfo
	for _, m := range reDshowOption.FindAllStringSubmatch(out, -1) {
		format := ""
		switch {
		case m[1] == "vcodec" && m[2] == "mjpeg":
			format = "MJPG"
		case m[1] == "pixel_format" && m[2] == "yuyv422":
			format = "YUYV"
		default:
			continue
		}
		w, _ := strconv.Atoi(m[6])
		h, _ := strconv.Atoi(m[7])
		lo, _ := strconv.ParseFloat(m[5], 64)
		hi, _ := strconv.ParseFloat(m[8], 64)
		modes = addMode(modes, format, w, h, fpsRange(lo, hi))
	}
	return modes
}

// parseAVFModes parses the "Supported modes" list ffmpeg prints when an
// unsupported size is requested from an AVFoundation device. AVFoundation
// delivers decoded frames, so the format is RAW (encoded to JPEG by ffmpeg).
func parseAVFModes(out string) []videoModeInfo {
	var modes []videoModeInfo
	for _, m := range reAVFMode.FindAllStringSubmatch(out, -1) {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		lo, _ := strconv.ParseFloat(m[3], 64)
		hi, _ := strconv.ParseFloat(m[4], 64)
		modes = addMode(modes, "RAW", w, h, fpsRange(lo, hi))
	}
	return modes
}

// mjpegQScale maps a JPEG quality (10..100) to ffmpeg's -q:v (31..2).
func mjpegQScale(quality int) int {
	q := 2 + int(math.Round(float64(100-quality)*29/90))
	return min(max(q, 2), 31)
}
