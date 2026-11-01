package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// videoEncoder is one way to encode recordings: a GPU encoder when the
// system has a working one, otherwise software.
type videoEncoder struct {
	Name  string `json:"name"`  // ffmpeg encoder
	Codec string `json:"codec"` // "h264" or "h265"
	GPU   bool   `json:"gpu"`
	// global options placed before the inputs (e.g. the VAAPI device)
	pre []string
	// encoder options for a quality level ("high", "normal", "small")
	args func(quality string) []string
}

var codecNames = map[string]string{"h264": "H.264", "h265": "H.265"}

// level picks the value for a quality level from (high, normal, small).
func level(quality string, high, normal, small int) string {
	switch quality {
	case "high":
		return strconv.Itoa(high)
	case "small":
		return strconv.Itoa(small)
	}
	return strconv.Itoa(normal)
}

// Software fallbacks, chosen to keep up with 1080p30 in real time.
// Quality levels keep console text sharp (SSIM >= 0.997 measured).
var softwareEncoders = map[string]videoEncoder{
	"h264": {Name: "libx264", Codec: "h264", args: func(q string) []string {
		return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", level(q, 20, 23, 28), "-pix_fmt", "yuv420p"}
	}},
	"h265": {Name: "libx265", Codec: "h265", args: func(q string) []string {
		return []string{"-c:v", "libx265", "-preset", "ultrafast", "-crf", level(q, 22, 26, 28), "-pix_fmt", "yuv420p",
			"-x265-params", "log-level=error"}
	}},
}

// gpuEncoders lists the GPU encoders worth trying for a codec on this
// system, most preferred first.
func gpuEncoders(codec string) []videoEncoder {
	hevc := codec == "h265"
	name := func(h264, h265 string) string {
		if hevc {
			return h265
		}
		return h264
	}
	// GPU quality scales: H.265 24/28/32, H.264 20/24/28
	q := func(quality string) string {
		if hevc {
			return level(quality, 24, 28, 32)
		}
		return level(quality, 20, 24, 28)
	}
	nvenc := videoEncoder{Name: name("h264_nvenc", "hevc_nvenc"), Codec: codec, GPU: true}
	nvenc.args = func(quality string) []string {
		return []string{"-c:v", nvenc.Name, "-preset", "p4", "-rc", "vbr", "-cq", q(quality), "-b:v", "0", "-pix_fmt", "yuv420p"}
	}
	var c []videoEncoder
	switch runtime.GOOS {
	case "linux":
		if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
			va := videoEncoder{Name: name("h264_vaapi", "hevc_vaapi"), Codec: codec, GPU: true,
				pre: []string{"-vaapi_device", "/dev/dri/renderD128"}}
			va.args = func(quality string) []string {
				return []string{"-vf", "format=nv12,hwupload", "-c:v", va.Name, "-rc_mode", "CQP", "-qp", q(quality)}
			}
			c = append(c, va)
		}
		c = append(c, nvenc)
	case "windows":
		qsv := videoEncoder{Name: name("h264_qsv", "hevc_qsv"), Codec: codec, GPU: true}
		qsv.args = func(quality string) []string {
			return []string{"-c:v", qsv.Name, "-global_quality", q(quality), "-pix_fmt", "nv12"}
		}
		amf := videoEncoder{Name: name("h264_amf", "hevc_amf"), Codec: codec, GPU: true}
		amf.args = func(quality string) []string {
			return []string{"-c:v", amf.Name, "-rc", "cqp", "-qp_i", q(quality), "-qp_p", q(quality), "-pix_fmt", "yuv420p"}
		}
		c = append(c, nvenc, qsv, amf)
	case "darwin":
		vt := name("h264_videotoolbox", "hevc_videotoolbox")
		c = append(c,
			// constant quality (Apple Silicon)
			videoEncoder{Name: vt, Codec: codec, GPU: true, args: func(quality string) []string {
				return []string{"-c:v", vt, "-q:v", level(quality, 70, 60, 50), "-pix_fmt", "yuv420p"}
			}},
			// bitrate mode (Intel Macs, where -q:v is not supported)
			videoEncoder{Name: vt, Codec: codec, GPU: true, args: func(quality string) []string {
				return []string{"-c:v", vt, "-b:v", level(quality, 8000000, 4000000, 2000000), "-pix_fmt", "yuv420p"}
			}})
	}
	return c
}

// probeEncoder checks that enc really works (driver, hardware) by encoding a
// few frames of a test pattern.
func probeEncoder(ffmpeg string, enc videoEncoder) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, enc.pre...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30", "-frames:v", "5")
	args = append(args, enc.args("normal")...)
	args = append(args, "-f", "null", "-")
	return exec.CommandContext(ctx, ffmpeg, args...).Run() == nil
}

// encoderPicker detects, once and in the background, the GPU encoder that
// works for each codec (if any). Software encoders are always available.
type encoderPicker struct {
	done chan struct{}
	gpu  map[string]videoEncoder // codec -> first working GPU encoder
}

// Encoder modes.
const (
	encoderAuto     = "auto"     // GPU when available, else software
	encoderHardware = "hardware" // GPU only
	encoderSoftware = "software" // CPU only
)

func newEncoderPicker(ffmpeg string) *encoderPicker {
	p := &encoderPicker{done: make(chan struct{}), gpu: map[string]videoEncoder{}}
	if ffmpeg == "" {
		close(p.done)
		return p
	}
	go func() {
		defer close(p.done)
		for _, codec := range []string{"h265", "h264"} {
			for _, e := range gpuEncoders(codec) {
				if probeEncoder(ffmpeg, e) {
					p.gpu[codec] = e
					break
				}
			}
			e, ok := p.gpu[codec]
			slog.Info("recording encoders", "codec", codecNames[codec], "gpu", ok, "gpuEncoder", e.Name, "software", softwareEncoders[codec].Name)
		}
	}()
	return p
}

// get waits for detection and returns the encoder for codec and mode.
func (p *encoderPicker) get(codec, mode string) (videoEncoder, error) {
	sw, ok := softwareEncoders[codec]
	if !ok {
		return videoEncoder{}, fmt.Errorf("unknown codec %q (use h264 or h265)", codec)
	}
	<-p.done
	gpu, hasGPU := p.gpu[codec]
	switch mode {
	case "", encoderAuto:
		if hasGPU {
			return gpu, nil
		}
		return sw, nil
	case encoderHardware:
		if !hasGPU {
			return videoEncoder{}, fmt.Errorf("no working GPU encoder for %s on this system", codecNames[codec])
		}
		return gpu, nil
	case encoderSoftware:
		return sw, nil
	}
	return videoEncoder{}, fmt.Errorf("unknown encoder mode %q (use auto, hardware or software)", mode)
}

// encoderChoice describes, per codec, what each mode would use.
type encoderChoice struct {
	GPU      *videoEncoder `json:"gpu"` // nil: no working GPU encoder
	Software videoEncoder  `json:"software"`
}

// all returns the detected encoders without waiting ("ready" is false while
// detection is still running).
func (p *encoderPicker) all() (map[string]encoderChoice, bool) {
	select {
	case <-p.done:
	default:
		return nil, false
	}
	out := map[string]encoderChoice{}
	for codec, sw := range softwareEncoders {
		c := encoderChoice{Software: sw}
		if g, ok := p.gpu[codec]; ok {
			c.GPU = &g
		}
		out[codec] = c
	}
	return out, true
}
