package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func solidJPEG(t *testing.T, c uint8) []byte {
	img := image.NewGray(image.Rect(0, 0, 320, 240))
	for i := range img.Pix {
		img.Pix[i] = c
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func toneChunk(on bool, phase *float64) []byte {
	b := make([]byte, audioChunk)
	for i := 0; i < audioChunk/4; i++ {
		var v int16
		if on {
			v = int16(12000 * math.Sin(*phase))
		}
		*phase += 2 * math.Pi * 1000 / audioRate
		binary.LittleEndian.PutUint16(b[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(b[i*4+2:], uint16(v))
	}
	return b
}

// firstMatch runs ffmpeg with a filter and returns the first number captured.
func firstMatch(t *testing.T, args []string, re string) float64 {
	out, _ := exec.Command("ffmpeg", args...).CombinedOutput()
	m := regexp.MustCompile(re).FindSubmatch(out)
	if m == nil {
		t.Fatalf("pattern %q not found in ffmpeg output:\n%s", re, out)
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return v
}

func TestRecorderSync(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	black, white := solidJPEG(t, 0), solidJPEG(t, 255)

	for _, format := range []string{"mp4", "mkv"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			if keep := os.Getenv("HDMIVIEW_KEEP"); keep != "" {
				dir = keep
			}
			video, audio := newHub(1), newHub(50)
			rec := newRecorder(newFileStore(dir, ".mp4", ".mkv"), video, audio)
			name, err := rec.start(format, "normal", 30)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond) // let feeders subscribe

			start := time.Now()
			done := make(chan struct{})
			go func() { // audio: starts 300 ms late, 1 kHz tone at 1.0..1.2 s
				defer close(done)
				time.Sleep(300 * time.Millisecond)
				var phase float64
				next := start.Add(300 * time.Millisecond)
				for time.Since(start) < 3*time.Second {
					at := next.Sub(start)
					audio.publish(toneChunk(at >= time.Second && at < 1200*time.Millisecond, &phase))
					next = next.Add(10 * time.Millisecond)
					time.Sleep(time.Until(next))
				}
			}()
			for k := 0; k < 90; k++ { // video: 30 fps, frames 30..35 (1.0..1.2 s) white
				f := black
				if k >= 30 && k < 36 {
					f = white
				}
				video.publish(f)
				time.Sleep(time.Until(start.Add(time.Duration(k+1) * time.Second / 30)))
			}
			<-done
			if err := rec.stopRecording(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)

			white0 := firstMatch(t, []string{"-hide_banner", "-i", path, "-vf", "blackdetect=d=0.01:pix_th=0.5", "-an", "-f", "null", "-"},
				`black_end:([0-9.]+)`)
			tone0 := firstMatch(t, []string{"-hide_banner", "-i", path, "-af", "silencedetect=n=-30dB:d=0.05", "-vn", "-f", "null", "-"},
				`silence_end: ([0-9.]+)`)
			vdur := firstMatch(t, []string{"-hide_banner", "-i", path, "-map", "0:v", "-f", "null", "-"}, `time=00:00:([0-9.]+)`)
			adur := firstMatch(t, []string{"-hide_banner", "-i", path, "-map", "0:a", "-f", "null", "-"}, `time=00:00:([0-9.]+)`)
			t.Logf("%s: white frame at %.3fs, tone at %.3fs, video %.2fs, audio %.2fs", format, white0, tone0, vdur, adur)

			if d := math.Abs(white0 - tone0); d > 0.04 {
				t.Errorf("A/V offset %.0f ms (want < 40 ms)", d*1000)
			}
			if d := math.Abs(white0 - 1.0); d > 0.04 {
				t.Errorf("white frame at %.3fs, want ~1.0s", white0)
			}
			if d := math.Abs(vdur - adur); d > 0.15 {
				t.Errorf("video %.2fs vs audio %.2fs", vdur, adur)
			}
		})
	}
}
