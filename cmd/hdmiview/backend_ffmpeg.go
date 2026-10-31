//go:build !linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// ffmpegBackend captures with ffmpeg: DirectShow on Windows, AVFoundation
// on macOS. Video comes out of ffmpeg as an MJPEG stream (the chip's own
// MJPEG copied, or raw frames encoded), audio as raw PCM. Picture controls
// go straight to the chip over HID (controls_hid.go).
type ffmpegBackend struct {
	ffmpeg              string
	video, audio, match string // --video, --audio, --match

	mu    sync.Mutex
	devs  []ffDevice                 // last device listing
	modes map[string][]videoModeInfo // per device name
}

func newBackend(video, audio, match string) (captureBackend, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("capture is not implemented on %s", runtime.GOOS)
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("ffmpeg was not found in PATH; it is required for capture on " + runtime.GOOS)
	}
	return &ffmpegBackend{ffmpeg: ff, video: video, audio: audio, match: match, modes: map[string][]videoModeInfo{}}, nil
}

// probe runs ffmpeg for a listing; these commands exit with an error by
// design, so only the output matters.
func (b *ffmpegBackend) probe(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, b.ffmpeg, append([]string{"-hide_banner", "-nostdin"}, args...)...).CombinedOutput()
	return string(out)
}

func (b *ffmpegBackend) listDevices() []ffDevice {
	var devs []ffDevice
	if runtime.GOOS == "windows" {
		devs = parseDshowDevices(b.probe("-list_devices", "true", "-f", "dshow", "-i", "dummy"))
	} else {
		devs = parseAVFDevices(b.probe("-f", "avfoundation", "-list_devices", "true", "-i", ""))
	}
	b.mu.Lock()
	b.devs = devs
	b.mu.Unlock()
	return devs
}

// device finds a device by name, listing again if it is not known yet.
func (b *ffmpegBackend) device(kind, name string) (ffDevice, error) {
	b.mu.Lock()
	devs := b.devs
	b.mu.Unlock()
	for _, d := range devs {
		if d.Kind == kind && d.Name == name {
			return d, nil
		}
	}
	return pickDevice(b.listDevices(), kind, name, nil)
}

func (b *ffmpegBackend) videoDevice() (string, error) {
	d, err := pickDevice(b.listDevices(), "video", b.video, []string{b.match, "MS2109", "USB Video"})
	return d.Name, err
}

func (b *ffmpegBackend) audioDevice() (string, error) {
	d, err := pickDevice(b.listDevices(), "audio", b.audio, []string{b.match, "MS2109", "USB Digital Audio"})
	return d.Name, err
}

// input returns the ffmpeg input arguments selecting device d.
func input(d ffDevice) []string {
	if runtime.GOOS == "windows" {
		spec := d.Name
		if d.Alt != "" {
			spec = d.Alt // unique even if several devices share a name
		}
		return []string{"-f", "dshow", "-i", d.Kind + "=" + spec}
	}
	if d.Kind == "video" {
		return []string{"-f", "avfoundation", "-i", d.Alt + ":none"}
	}
	return []string{"-f", "avfoundation", "-i", ":" + d.Alt}
}

func (b *ffmpegBackend) listModes(dev string) ([]videoModeInfo, error) {
	b.mu.Lock()
	cached := b.modes[dev]
	b.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	d, err := b.device("video", dev)
	if err != nil {
		return nil, err
	}
	var modes []videoModeInfo
	if runtime.GOOS == "windows" {
		in := input(d)
		modes = parseDshowOptions(b.probe(append([]string{"-list_options", "true"}, in...)...))
	} else {
		// Asking for an impossible size makes AVFoundation list the supported modes.
		modes = parseAVFModes(b.probe(append([]string{"-video_size", "1x1"}, input(d)...)...))
	}
	if len(modes) == 0 {
		return nil, fmt.Errorf("could not read the video modes of %q (is it in use?)", dev)
	}
	b.mu.Lock()
	b.modes[dev] = modes
	b.mu.Unlock()
	return modes, nil
}

func (b *ffmpegBackend) openVideo(dev string, mode videoMode) (videoStream, error) {
	d, err := b.device("video", dev)
	if err != nil {
		return nil, err
	}
	size := fmt.Sprintf("%dx%d", mode.Width, mode.Height)
	fps := strconv.Itoa(mode.FPS)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if runtime.GOOS == "windows" {
		args = append(args, "-rtbufsize", "100M", "-video_size", size, "-framerate", fps)
		if mode.Format == "MJPG" {
			args = append(args, "-vcodec", "mjpeg")
		} else {
			args = append(args, "-pixel_format", "yuyv422")
		}
	} else {
		args = append(args, "-video_size", size, "-framerate", fps)
	}
	args = append(args, input(d)...)
	if mode.Format == "MJPG" {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-c:v", "mjpeg", "-q:v", strconv.Itoa(mjpegQScale(mode.Quality)), "-pix_fmt", "yuvj422p")
	}
	args = append(args, "-f", "mjpeg", "pipe:1")

	s, err := startFFmpeg(b.ffmpeg, args)
	if err != nil {
		return nil, err
	}
	slog.Info("video capture started", "device", dev, "format", mode.Format, "width", mode.Width, "height", mode.Height, "fps", mode.FPS)
	return &ffmpegVideo{ffmpegProc: s, r: newMJPEGReader(s.out)}, nil
}

func (b *ffmpegBackend) runAudio(dev string, h *hub, stop <-chan struct{}) error {
	d, err := b.device("audio", dev)
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if runtime.GOOS == "windows" {
		args = append(args, "-audio_buffer_size", "20") // ms; the default is 500
	}
	args = append(args, input(d)...)
	args = append(args, "-f", "s16le", "-ar", strconv.Itoa(audioRate), "-ac", strconv.Itoa(audioChannels), "pipe:1")
	s, err := startFFmpeg(b.ffmpeg, args)
	if err != nil {
		return err
	}
	slog.Info("audio capture started", "device", dev, "rate", audioRate, "channels", audioChannels)
	go func() {
		<-stop
		s.close()
	}()
	for {
		buf := make([]byte, audioChunk)
		if _, err := io.ReadFull(s.out, buf); err != nil {
			s.close()
			select {
			case <-stop:
				return nil
			default:
				return s.failure(err)
			}
		}
		h.publish(buf)
	}
}

// ---------------------------------------------------------------- processes

type ffmpegProc struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr *tailBuffer
	once   sync.Once
}

func startFFmpeg(ff string, args []string) (*ffmpegProc, error) {
	cmd := exec.Command(ff, args...)
	p := &ffmpegProc{cmd: cmd, stderr: &tailBuffer{max: 2048}}
	cmd.Stderr = p.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p.out = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *ffmpegProc) close() {
	p.once.Do(func() {
		p.cmd.Process.Kill()
		p.cmd.Wait()
	})
}

// failure describes why the stream ended, using ffmpeg's last message.
func (p *ffmpegProc) failure(err error) error {
	if msg := lastLine(p.stderr.String()); msg != "" {
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return fmt.Errorf("ffmpeg: %w", err)
}

type ffmpegVideo struct {
	*ffmpegProc
	r *mjpegReader
}

func (v *ffmpegVideo) next() ([]byte, error) {
	f, err := v.r.next()
	if err != nil {
		v.close()
		return nil, v.failure(err)
	}
	return f, nil
}
