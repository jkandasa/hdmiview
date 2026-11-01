//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Minimal V4L2 capture (mmap streaming I/O; the two structs whose layout
// differs between 32- and 64-bit ABIs live in v4l2_32.go / v4l2_64.go):
// MJPEG frames are passed through, YUYV frames are encoded to JPEG.

const (
	bufTypeVideoCapture = 1
	memoryMMAP          = 1
	numBuffers          = 4
	frmTypeDiscrete     = 1

	ctrlFlagNextCtrl = 0x80000000
	ctrlFlagDisabled = 0x0001
	ctrlFlagReadOnly = 0x0004
	ctrlFlagInactive = 0x0010
	ctrlTypeInteger  = 1
	ctrlTypeBoolean  = 2
	ctrlTypeMenu     = 3
)

func fourcc(s string) uint32 {
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

func fourccString(v uint32) string {
	return string([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

var (
	pixFmtMJPEG = fourcc("MJPG")
	pixFmtYUYV  = fourcc("YUYV")
)

func iowr(nr, size uintptr) uintptr { return 3<<30 | size<<16 | 'V'<<8 | nr }
func iow(nr, size uintptr) uintptr  { return 1<<30 | size<<16 | 'V'<<8 | nr }

type v4l2PixFormat struct {
	Width, Height, PixelFormat, Field, BytesPerLine, SizeImage uint32
	Colorspace, Priv, Flags, YcbcrEnc, Quantization, XferFunc  uint32
}

type v4l2Streamparm struct {
	Type                      uint32
	Capability, CaptureMode   uint32
	Numerator, Denominator    uint32
	ExtendedMode, ReadBuffers uint32
	_                         [4]uint32
	_                         [200 - 40]byte
}

type v4l2RequestBuffers struct {
	Count, Type, Memory, Capabilities uint32
	Flags                             uint8
	_                                 [3]uint8
}

type v4l2FmtDesc struct {
	Index, Type, Flags    uint32
	Description           [32]byte
	PixelFormat, MbusCode uint32
	_                     [3]uint32
}

type v4l2FrmSizeEnum struct {
	Index, PixelFormat, Type uint32
	Width, Height            uint32 // discrete member of the union
	_                        [4]uint32
	_                        [2]uint32
}

type v4l2FrmIvalEnum struct {
	Index, PixelFormat, Width, Height, Type uint32
	Numerator, Denominator                  uint32 // discrete member of the union
	_                                       [4]uint32
	_                                       [2]uint32
}

type v4l2QueryCtrl struct {
	ID, Type                   uint32
	Name                       [32]byte
	Min, Max, Step, DefaultVal int32
	Flags                      uint32
	_                          [2]uint32
}

type v4l2Control struct {
	ID    uint32
	Value int32
}

type v4l2QueryMenu struct {
	ID, Index uint32
	Name      [32]byte
	_         uint32
}

func init() {
	// Guard against a struct layout that does not match the kernel ABI.
	sizes := []struct {
		got, want uintptr
	}{
		{unsafe.Sizeof(v4l2Format{}), sizeofV4L2Format}, {unsafe.Sizeof(v4l2Streamparm{}), 204},
		{unsafe.Sizeof(v4l2RequestBuffers{}), 20}, {unsafe.Sizeof(v4l2Buffer{}), sizeofV4L2Buffer},
		{unsafe.Sizeof(v4l2FmtDesc{}), 64}, {unsafe.Sizeof(v4l2FrmSizeEnum{}), 44},
		{unsafe.Sizeof(v4l2FrmIvalEnum{}), 52}, {unsafe.Sizeof(v4l2QueryCtrl{}), 68},
		{unsafe.Sizeof(v4l2Control{}), 8}, {unsafe.Sizeof(v4l2QueryMenu{}), 44},
	}
	for _, s := range sizes {
		if s.got != s.want {
			panic("unsupported architecture: V4L2 struct layout mismatch")
		}
	}
}

var (
	vidiocEnumFmt        = iowr(2, unsafe.Sizeof(v4l2FmtDesc{}))
	vidiocSFmt           = iowr(5, unsafe.Sizeof(v4l2Format{}))
	vidiocReqBufs        = iowr(8, unsafe.Sizeof(v4l2RequestBuffers{}))
	vidiocQueryBuf       = iowr(9, unsafe.Sizeof(v4l2Buffer{}))
	vidiocQBuf           = iowr(15, unsafe.Sizeof(v4l2Buffer{}))
	vidiocDQBuf          = iowr(17, unsafe.Sizeof(v4l2Buffer{}))
	vidiocStreamOn       = iow(18, 4)
	vidiocSParm          = iowr(22, unsafe.Sizeof(v4l2Streamparm{}))
	vidiocGCtrl          = iowr(27, unsafe.Sizeof(v4l2Control{}))
	vidiocSCtrl          = iowr(28, unsafe.Sizeof(v4l2Control{}))
	vidiocQueryCtrl      = iowr(36, unsafe.Sizeof(v4l2QueryCtrl{}))
	vidiocQueryMenu      = iowr(37, unsafe.Sizeof(v4l2QueryMenu{}))
	vidiocEnumFrameSizes = iowr(74, unsafe.Sizeof(v4l2FrmSizeEnum{}))
	vidiocEnumFrameIvals = iowr(75, unsafe.Sizeof(v4l2FrmIvalEnum{}))
)

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// findVideoDevice returns the capture node of the first device whose name
// contains match (each UVC device also exposes a metadata node, which has a
// higher index, so the lowest index is the capture node).
func findVideoDevice(match string) (string, error) {
	nodes, _ := filepath.Glob("/sys/class/video4linux/video*")
	best, bestNum := "", -1
	for _, n := range nodes {
		name, err := os.ReadFile(filepath.Join(n, "name"))
		if err != nil || !strings.Contains(string(name), match) {
			continue
		}
		idx, _ := os.ReadFile(filepath.Join(n, "index"))
		if strings.TrimSpace(string(idx)) != "0" {
			continue
		}
		var num int
		fmt.Sscanf(filepath.Base(n), "video%d", &num)
		if bestNum < 0 || num < bestNum {
			best, bestNum = "/dev/"+filepath.Base(n), num
		}
	}
	if best == "" {
		return "", fmt.Errorf("no video device matching %q", match)
	}
	return best, nil
}

// ---------------------------------------------------------------- enumeration

// listModes enumerates the supported formats, discrete frame sizes and
// frame rates. Only MJPEG and YUYV are reported (the formats we can serve).
func listModes(path string) ([]videoModeInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)

	var modes []videoModeInfo
	for fi := uint32(0); ; fi++ {
		fd0 := v4l2FmtDesc{Index: fi, Type: bufTypeVideoCapture}
		if ioctl(fd, vidiocEnumFmt, unsafe.Pointer(&fd0)) != nil {
			break
		}
		if fd0.PixelFormat != pixFmtMJPEG && fd0.PixelFormat != pixFmtYUYV {
			continue
		}
		for si := uint32(0); ; si++ {
			fs := v4l2FrmSizeEnum{Index: si, PixelFormat: fd0.PixelFormat}
			if ioctl(fd, vidiocEnumFrameSizes, unsafe.Pointer(&fs)) != nil || fs.Type != frmTypeDiscrete {
				break
			}
			m := videoModeInfo{Format: fourccString(fd0.PixelFormat), Width: int(fs.Width), Height: int(fs.Height)}
			for ii := uint32(0); ; ii++ {
				iv := v4l2FrmIvalEnum{Index: ii, PixelFormat: fd0.PixelFormat, Width: fs.Width, Height: fs.Height}
				if ioctl(fd, vidiocEnumFrameIvals, unsafe.Pointer(&iv)) != nil || iv.Type != frmTypeDiscrete {
					break
				}
				if iv.Numerator > 0 {
					m.FPS = append(m.FPS, int((iv.Denominator+iv.Numerator/2)/iv.Numerator))
				}
			}
			modes = append(modes, m)
		}
	}
	if len(modes) == 0 {
		return nil, errors.New("device reports no MJPEG/YUYV modes")
	}
	return modes, nil
}

// ---------------------------------------------------------------- controls

func listControls(path string) ([]videoControl, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)

	var out []videoControl
	id := uint32(ctrlFlagNextCtrl)
	for {
		q := v4l2QueryCtrl{ID: id}
		if ioctl(fd, vidiocQueryCtrl, unsafe.Pointer(&q)) != nil {
			break
		}
		id = q.ID | ctrlFlagNextCtrl
		if q.Flags&ctrlFlagDisabled != 0 {
			continue
		}
		c := videoControl{ID: q.ID, Name: cstr(q.Name[:]), Min: q.Min, Max: q.Max, Step: q.Step,
			Default: q.DefaultVal, ReadOnly: q.Flags&(ctrlFlagReadOnly|ctrlFlagInactive) != 0}
		switch q.Type {
		case ctrlTypeInteger:
			c.Type = "int"
		case ctrlTypeBoolean:
			c.Type = "bool"
		case ctrlTypeMenu:
			c.Type = "menu"
			c.Menu = map[int]string{}
			for i := q.Min; i <= q.Max; i++ {
				m := v4l2QueryMenu{ID: q.ID, Index: uint32(i)}
				if ioctl(fd, vidiocQueryMenu, unsafe.Pointer(&m)) == nil {
					c.Menu[int(i)] = cstr(m.Name[:])
				}
			}
		default:
			continue // buttons, control classes, ...
		}
		v := v4l2Control{ID: q.ID}
		if ioctl(fd, vidiocGCtrl, unsafe.Pointer(&v)) == nil {
			c.Value = v.Value
		}
		out = append(out, c)
	}
	return out, nil
}

func setControl(path string, id uint32, value int32) (int32, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer syscall.Close(fd)
	c := v4l2Control{ID: id, Value: value}
	if err := ioctl(fd, vidiocSCtrl, unsafe.Pointer(&c)); err != nil {
		return 0, err
	}
	if err := ioctl(fd, vidiocGCtrl, unsafe.Pointer(&c)); err != nil {
		return 0, err
	}
	return c.Value, nil
}

// ---------------------------------------------------------------- capture

type videoCapture struct {
	fd      int
	bufs    [][]byte
	mode    videoMode
	pixFmt  uint32
	scratch []byte
}

func openVideo(path string, mode videoMode) (*videoCapture, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	v := &videoCapture{fd: fd, mode: mode, pixFmt: fourcc(mode.Format)}
	fail := func(what string, err error) (*videoCapture, error) {
		v.close()
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	f := v4l2Format{Type: bufTypeVideoCapture}
	f.Pix.Width, f.Pix.Height, f.Pix.PixelFormat = uint32(mode.Width), uint32(mode.Height), v.pixFmt
	if err := ioctl(fd, vidiocSFmt, unsafe.Pointer(&f)); err != nil {
		return fail("set format", err)
	}
	if f.Pix.PixelFormat != v.pixFmt || f.Pix.Width != uint32(mode.Width) || f.Pix.Height != uint32(mode.Height) {
		return fail("set format", fmt.Errorf("device chose %s %dx%d", fourccString(f.Pix.PixelFormat), f.Pix.Width, f.Pix.Height))
	}

	p := v4l2Streamparm{Type: bufTypeVideoCapture, Numerator: 1, Denominator: uint32(mode.FPS)}
	if err := ioctl(fd, vidiocSParm, unsafe.Pointer(&p)); err != nil {
		return fail("set frame rate", err)
	}

	rb := v4l2RequestBuffers{Count: numBuffers, Type: bufTypeVideoCapture, Memory: memoryMMAP}
	if err := ioctl(fd, vidiocReqBufs, unsafe.Pointer(&rb)); err != nil {
		return fail("request buffers", err)
	}
	for i := uint32(0); i < rb.Count; i++ {
		b := v4l2Buffer{Index: i, Type: bufTypeVideoCapture, Memory: memoryMMAP}
		if err := ioctl(fd, vidiocQueryBuf, unsafe.Pointer(&b)); err != nil {
			return fail("query buffer", err)
		}
		m, err := syscall.Mmap(fd, int64(b.Offset), int(b.Length), syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return fail("mmap", err)
		}
		v.bufs = append(v.bufs, m)
		if err := ioctl(fd, vidiocQBuf, unsafe.Pointer(&b)); err != nil {
			return fail("queue buffer", err)
		}
	}
	t := uint32(bufTypeVideoCapture)
	if err := ioctl(fd, vidiocStreamOn, unsafe.Pointer(&t)); err != nil {
		return fail("stream on", err)
	}
	slog.Debug("v4l2 streaming", "device", path, "format", mode.Format,
		"width", f.Pix.Width, "height", f.Pix.Height, "fps", float64(p.Denominator)/float64(max(p.Numerator, 1)))
	return v, nil
}

// next blocks for the next frame and returns it as a complete JPEG, or nil
// for a frame that is empty/truncated (the MS2109 emits some).
func (v *videoCapture) next() ([]byte, error) {
	b := v4l2Buffer{Type: bufTypeVideoCapture, Memory: memoryMMAP}
	if err := ioctl(v.fd, vidiocDQBuf, unsafe.Pointer(&b)); err != nil {
		return nil, err
	}
	data := v.bufs[b.Index][:b.BytesUsed]
	var frame []byte
	isYUYV := v.pixFmt == pixFmtYUYV
	if isYUYV {
		if len(data) == v.mode.Width*v.mode.Height*2 {
			v.scratch = append(v.scratch[:0], data...)
		} else {
			v.scratch = v.scratch[:0]
		}
	} else if len(data) > 4 && data[0] == 0xFF && data[1] == 0xD8 {
		if end := bytes.LastIndex(data, []byte{0xFF, 0xD9}); end > 0 {
			frame = bytes.Clone(data[:end+2])
		}
	}
	if err := ioctl(v.fd, vidiocQBuf, unsafe.Pointer(&b)); err != nil {
		return nil, err
	}
	if isYUYV && len(v.scratch) > 0 {
		return encodeYUYV(v.scratch, v.mode.Width, v.mode.Height, v.mode.Quality)
	}
	return frame, nil
}

// encodeYUYV converts packed YUYV 4:2:2 into planar YCbCr 4:2:2 and JPEG-encodes it.
func encodeYUYV(src []byte, w, h, quality int) ([]byte, error) {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio422)
	for y := 0; y < h; y++ {
		row := src[y*w*2 : (y+1)*w*2]
		yl := img.Y[y*img.YStride:]
		cl := img.Cb[y*img.CStride:]
		rl := img.Cr[y*img.CStride:]
		for x := 0; x < w/2; x++ {
			p := row[x*4 : x*4+4]
			yl[2*x], cl[x], yl[2*x+1], rl[x] = p[0], p[1], p[2], p[3]
		}
	}
	var buf bytes.Buffer
	buf.Grow(w * h / 4)
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (v *videoCapture) close() {
	for _, m := range v.bufs {
		syscall.Munmap(m)
	}
	syscall.Close(v.fd) // closing also stops streaming
}
