package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"testing"
	"testing/iotest"
)

func testJPEG(t *testing.T, seed byte) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for i := range img.Pix {
		img.Pix[i] = byte(i)*seed + seed
	}
	img.Set(5, 5, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// trickyJPEG puts the bytes FF D9 (end-of-image) inside the quantization
// table, where a naive search for EOI would cut the frame short.
func trickyJPEG(t *testing.T) []byte {
	f := testJPEG(t, 3)
	i := bytes.Index(f, []byte{0xFF, 0xDB}) // DQT
	if i < 0 {
		t.Fatal("no DQT")
	}
	f[i+5], f[i+6] = 0xFF, 0xD9 // two table entries (precision/id byte is at i+4)
	if _, err := jpeg.Decode(bytes.NewReader(f)); err != nil {
		t.Fatalf("tricky JPEG no longer decodes: %v", err)
	}
	return f
}

func TestJPEGFrameLen(t *testing.T) {
	f := trickyJPEG(t)
	if n := jpegFrameLen(f); n != len(f) {
		t.Fatalf("frame length %d, want %d", n, len(f))
	}
	for cut := 0; cut < len(f); cut += 7 {
		if n := jpegFrameLen(f[:cut]); n != 0 {
			t.Fatalf("truncated frame (%d bytes) reported length %d", cut, n)
		}
	}
	if jpegFrameLen([]byte("garbage")) != -1 {
		t.Fatal("garbage accepted")
	}
}

func TestMJPEGReader(t *testing.T) {
	frames := [][]byte{testJPEG(t, 1), trickyJPEG(t), testJPEG(t, 7)}
	var stream []byte
	for i, f := range frames {
		if i == 1 {
			stream = append(stream, "junk between frames"...)
		}
		stream = append(stream, f...)
	}
	for name, r := range map[string]io.Reader{
		"whole":    bytes.NewReader(stream),
		"one byte": iotest.OneByteReader(bytes.NewReader(stream)),
		"halves":   iotest.HalfReader(bytes.NewReader(stream)),
	} {
		m := newMJPEGReader(r)
		for i, want := range frames {
			got, err := m.next()
			if err != nil {
				t.Fatalf("%s: frame %d: %v", name, i, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: frame %d differs (%d vs %d bytes)", name, i, len(got), len(want))
			}
		}
		if _, err := m.next(); !errors.Is(err, io.EOF) {
			t.Fatalf("%s: want EOF after last frame, got %v", name, err)
		}
	}
}

const dshowDevicesNew = `[dshow @ 0000020f8a2d4a40] "Integrated Camera" (video)
[dshow @ 0000020f8a2d4a40]   Alternative name "@device_pnp_\\?\usb#vid_04f2&pid_b6d9&mi_00#6&1"
[dshow @ 0000020f8a2d4a40] "USB Video" (video)
[dshow @ 0000020f8a2d4a40]   Alternative name "@device_pnp_\\?\usb#vid_534d&pid_2109&mi_00#7&2"
[dshow @ 0000020f8a2d4a40] "Microphone Array (Realtek(R) Audio)" (audio)
[dshow @ 0000020f8a2d4a40]   Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{AAA}"
[dshow @ 0000020f8a2d4a40] "Digital Audio Interface (MS2109-Pro)" (audio)
[dshow @ 0000020f8a2d4a40]   Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{BBB}"
dummy: Immediate exit requested`

const dshowDevicesOld = `[dshow @ 0x1] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0x1]  "USB Video"
[dshow @ 0x1]     Alternative name "@device_pnp_\\?\usb#vid_534d&pid_2109&mi_00#7&2"
[dshow @ 0x1] DirectShow audio devices
[dshow @ 0x1]  "Digital Audio Interface (USB Digital Audio)"
[dshow @ 0x1]     Alternative name "@device_cm_{33D9A762}\wave_{CCC}"`

func TestDshowDevices(t *testing.T) {
	for name, out := range map[string]string{"new": dshowDevicesNew, "old": dshowDevicesOld} {
		devs := parseDshowDevices(out)
		v, err := pickDevice(devs, "video", "", []string{"MS2109", "MS2109", "USB Video"})
		if err != nil || v.Name != "USB Video" || v.Alt == "" {
			t.Errorf("%s: video = %+v, %v", name, v, err)
		}
		a, err := pickDevice(devs, "audio", "", []string{"MS2109", "MS2109", "USB Digital Audio"})
		if err != nil || !bytes.Contains([]byte(a.Name), []byte("Digital Audio Interface")) {
			t.Errorf("%s: audio = %+v, %v", name, a, err)
		}
	}
	devs := parseDshowDevices(dshowDevicesNew)
	if d, err := pickDevice(devs, "video", "integrated", nil); err != nil || d.Name != "Integrated Camera" {
		t.Errorf("explicit choice = %+v, %v", d, err)
	}
	if _, err := pickDevice(devs, "video", "nothing like this", nil); err == nil {
		t.Error("unknown explicit device accepted")
	}
}

const avfDevices = `[AVFoundation indev @ 0x7f8] AVFoundation video devices:
[AVFoundation indev @ 0x7f8] [0] FaceTime HD Camera
[AVFoundation indev @ 0x7f8] [1] MS2109-Pro
[AVFoundation indev @ 0x7f8] [2] Capture screen 0
[AVFoundation indev @ 0x7f8] AVFoundation audio devices:
[AVFoundation indev @ 0x7f8] [0] MacBook Pro Microphone
[AVFoundation indev @ 0x7f8] [1] MS2109-Pro
: Input/output error`

func TestAVFDevices(t *testing.T) {
	devs := parseAVFDevices(avfDevices)
	v, err := pickDevice(devs, "video", "", []string{"MS2109", "MS2109", "USB Video"})
	if err != nil || v.Alt != "1" {
		t.Errorf("video = %+v, %v", v, err)
	}
	a, err := pickDevice(devs, "audio", "", []string{"MS2109", "MS2109", "USB Digital Audio"})
	if err != nil || a.Alt != "1" {
		t.Errorf("audio = %+v, %v", a, err)
	}
}

func TestDshowOptions(t *testing.T) {
	out := `[dshow @ 0x1] DirectShow video device options (from video devices)
[dshow @ 0x1]  Pin "Capture" (alternative pin name "0")
[dshow @ 0x1]   vcodec=mjpeg  min s=1920x1080 fps=5 max s=1920x1080 fps=30
[dshow @ 0x1]   vcodec=mjpeg  min s=1920x1080 fps=5 max s=1920x1080 fps=30 (tv, bt470bg/bt709/unknown, center)
[dshow @ 0x1]   vcodec=mjpeg  min s=1280x720 fps=10 max s=1280x720 fps=60
[dshow @ 0x1]   pixel_format=yuyv422  min s=1920x1080 fps=5 max s=1920x1080 fps=5
[dshow @ 0x1]   pixel_format=nv12  min s=640x480 fps=5 max s=640x480 fps=30`
	modes := parseDshowOptions(out)
	want := []videoModeInfo{
		{"MJPG", 1920, 1080, []int{30, 25, 20, 10, 5}},
		{"MJPG", 1280, 720, []int{60, 50, 30, 25, 20, 10}},
		{"YUYV", 1920, 1080, []int{5}},
	}
	if len(modes) != len(want) {
		t.Fatalf("modes = %+v", modes)
	}
	for i := range want {
		if modes[i].Format != want[i].Format || modes[i].Width != want[i].Width || !slices.Equal(modes[i].FPS, want[i].FPS) {
			t.Errorf("mode %d = %+v, want %+v", i, modes[i], want[i])
		}
	}
}

func TestAVFModes(t *testing.T) {
	out := `[avfoundation @ 0x1] Selected video size (1x1) is not supported by the device.
[avfoundation @ 0x1] Supported modes:
[avfoundation @ 0x1]   1920x1080@[5.000000 30.000000]fps
[avfoundation @ 0x1]   1280x720@[10.000000 60.000000]fps
[avfoundation @ 0x1]   640x480@[30.000000 30.000000]fps`
	modes := parseAVFModes(out)
	if len(modes) != 3 || modes[0].Format != "RAW" || !slices.Equal(modes[2].FPS, []int{30}) || modes[1].FPS[0] != 60 {
		t.Fatalf("modes = %+v", modes)
	}
}

func TestMJPEGQScale(t *testing.T) {
	for q, want := range map[int]int{100: 2, 90: 5, 10: 31, 0: 31, 150: 2} {
		if got := mjpegQScale(q); got != want {
			t.Errorf("quality %d -> %d, want %d", q, got, want)
		}
	}
}

// Real ffmpeg MJPEG output (what the Windows/macOS backend reads) splits
// into decodable frames.
func TestMJPEGReaderWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-frames:v", "45", "-c:v", "mjpeg", "-q:v", strconv.Itoa(mjpegQScale(90)), "-pix_fmt", "yuvj422p", "-f", "mjpeg", "pipe:1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	m := newMJPEGReader(out)
	n := 0
	for {
		f, err := m.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(f))
		if err != nil {
			t.Fatalf("frame %d does not decode: %v", n, err)
		}
		if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 360 {
			t.Fatalf("frame %d is %v", n, b)
		}
		n++
	}
	if n != 45 {
		t.Fatalf("got %d frames, want 45", n)
	}
}
