package main

import (
	"bytes"
	"image/jpeg"
	"testing"
)

// yuyvFrame builds a frame with horizontal luma ramp and constant chroma.
func yuyvFrame(w, h int, cb, cr byte) []byte {
	f := make([]byte, w*h*2)
	for y := 0; y < h; y++ {
		for x := 0; x < w/2; x++ {
			p := f[(y*w+2*x)*2:]
			p[0], p[1], p[2], p[3] = byte(2*x*255/w), cb, byte((2*x+1)*255/w), cr
		}
	}
	return f
}

func TestEncodeYUYV(t *testing.T) {
	const w, h = 64, 16
	out, err := encodeYUYV(yuyvFrame(w, h, 90, 240), w, h, 95)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("size %v", b)
	}
	// Left edge is dark, right edge bright, and Cr=240 makes it reddish.
	l, _, _, _ := img.At(0, h/2).RGBA()
	r, g, bl, _ := img.At(w-1, h/2).RGBA()
	if l >= r || r <= g || r <= bl {
		t.Fatalf("unexpected colours: left R=%d right RGB=%d,%d,%d", l>>8, r>>8, g>>8, bl>>8)
	}
}

func BenchmarkEncodeYUYV1080p(b *testing.B) {
	f := yuyvFrame(1920, 1080, 128, 128)
	for b.Loop() {
		if _, err := encodeYUYV(f, 1920, 1080, 90); err != nil {
			b.Fatal(err)
		}
	}
}
