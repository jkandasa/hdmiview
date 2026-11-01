package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// recorder writes the live stream to a file with ffmpeg. Video frames
// (JPEG) and raw PCM audio are fed through two pipes. Both timelines are
// built here from arrival times, so ffmpeg's own read delays (start-up,
// encoder stalls) cannot skew them: video is made constant-rate (repeat the
// last frame for a missing slot, drop extras) and audio is placed by sample
// count, padded with silence or trimmed when it drifts from the video clock.

type recordFormat struct {
	Ext   string
	Label string
	args  func(enc videoEncoder) []string // container options
	audio func(quality string) []string   // audio codec options
}

// Audio bitrates follow the quality level. The chip delivers 48 kHz 16-bit
// stereo (1536 kbit/s); Opus and AAC both shrink to almost nothing on
// silence, unlike MP3.
var recordFormats = map[string]recordFormat{
	"mp4": {".mp4", "MP4 (AAC audio)",
		func(enc videoEncoder) []string {
			// fragmented MP4 written in 2 s pieces: the size grows steadily
			// and an interrupted recording stays playable
			args := []string{"-movflags", "+empty_moov+default_base_moof", "-frag_duration", "2000000"}
			if enc.Codec == "h265" {
				args = append(args, "-tag:v", "hvc1") // so Apple players accept it
			}
			return args
		},
		func(q string) []string { return []string{"-c:a", "aac", "-b:a", level(q, 160, 128, 96) + "k"} },
	},
	"mkv": {".mkv", "MKV (Opus audio)",
		func(videoEncoder) []string { return nil },
		func(q string) []string { return []string{"-c:a", "libopus", "-b:a", level(q, 128, 96, 64) + "k"} },
	},
}

type recording struct {
	name, path, format, quality string
	audio                       bool
	encoder                     videoEncoder
	started                     time.Time
	cmd                         *exec.Cmd
	stop                        chan struct{}
	wg                          sync.WaitGroup
	exited                      chan struct{}
	stderr                      *tailBuffer
	frames                      atomic.Int64 // video frames written
}

type recorder struct {
	mu      sync.Mutex
	store   *fileStore
	ffmpeg  string
	video   *hub
	audio   *hub // nil when audio is disabled
	inputs  func(withAudio bool) (recInput, error)
	encoder *encoderPicker
	active  *recording
	lastErr string
}

func newRecorder(store *fileStore, video, audio *hub) *recorder {
	ff, _ := exec.LookPath("ffmpeg")
	return &recorder{store: store, ffmpeg: ff, video: video, audio: audio, inputs: newRecInputs, encoder: newEncoderPicker(ff)}
}

// recInput connects the recorder to ffmpeg's video and audio inputs.
type recInput interface {
	videoURL() string
	audioURL() string
	attach(cmd *exec.Cmd) error // before Start
	started()                   // after Start
	video() io.WriteCloser
	audio() (io.WriteCloser, error) // may wait for ffmpeg to connect
	abort()
}

type stamped struct {
	data []byte
	at   time.Time
}

// stampedFeed receives from a hub subscription and timestamps each item on
// arrival. It never blocks on the consumer, so stamps stay accurate.
func stampedFeed(h *hub, n int, stop <-chan struct{}) <-chan stamped {
	in := h.subscribeN(n)
	out := make(chan stamped, 4*n)
	go func() {
		defer h.unsubscribe(in)
		defer close(out)
		for {
			select {
			case <-stop:
				return
			case d := <-in:
				select {
				case out <- stamped{d, time.Now()}:
				default: // consumer hopelessly behind: drop
				}
			}
		}
	}()
	return out
}

func (r *recorder) start(format, quality, codec, encoderMode string, withAudio bool, fps int) (string, error) {
	if codec == "" {
		codec = "h265"
	}
	enc, err := r.encoder.get(codec, encoderMode)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ffmpeg == "" {
		return "", errors.New("ffmpeg not found in PATH")
	}
	if r.active != nil {
		return "", errors.New("already recording")
	}
	f, ok := recordFormats[format]
	if !ok {
		return "", fmt.Errorf("unknown format %q", format)
	}
	dir := r.store.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "hdmi-" + time.Now().Format("20060102-150405") + f.Ext
	rec := &recording{name: name, path: filepath.Join(dir, name), format: format, quality: quality, encoder: enc, audio: withAudio,
		started: time.Now(), stop: make(chan struct{}), exited: make(chan struct{}), stderr: &tailBuffer{max: 4096}}

	withAudio = withAudio && r.audio != nil
	in, err := r.inputs(withAudio)
	if err != nil {
		return "", err
	}
	if fps <= 0 {
		fps = 30
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, enc.pre...)
	args = append(args, "-thread_queue_size", "1024", "-f", "mjpeg", "-framerate", fmt.Sprint(fps), "-i", in.videoURL())
	if withAudio {
		args = append(args, "-thread_queue_size", "1024", "-f", "s16le",
			"-ar", fmt.Sprint(audioRate), "-ac", fmt.Sprint(audioChannels), "-i", in.audioURL(),
			"-map", "0:v", "-map", "1:a")
	}
	args = append(args, "-fps_mode", "cfr")
	args = append(args, enc.args(quality)...)
	args = append(args, "-g", fmt.Sprint(fps*60), "-bf", "0")
	args = append(args, f.args(enc)...)
	if withAudio {
		args = append(args, f.audio(quality)...)
	}
	args = append(args, rec.path)

	rec.cmd = exec.Command(r.ffmpeg, args...)
	rec.cmd.Stderr = rec.stderr
	if err := in.attach(rec.cmd); err != nil {
		in.abort()
		return "", err
	}
	if err := rec.cmd.Start(); err != nil {
		in.abort()
		return "", err
	}
	in.started()
	vw := in.video()

	// Both feeders measure time from t0, the arrival of the first frame.
	t0ch := make(chan time.Time, 1)
	frames := stampedFeed(r.video, 60, rec.stop)
	var chunks <-chan stamped
	if withAudio {
		chunks = stampedFeed(r.audio, 400, rec.stop)
	}

	rec.wg.Add(1)
	go func() { // video: constant frame rate on the arrival-time grid
		defer rec.wg.Done()
		defer vw.Close()
		var t0 time.Time
		var last []byte
		next := 0 // index of the next output frame
		for f := range frames {
			if last == nil {
				t0 = f.at
				t0ch <- t0
			}
			slot := int(math.Round(f.at.Sub(t0).Seconds() * float64(fps)))
			for ; next < slot && last != nil; next++ { // missing frames: repeat
				if _, err := vw.Write(last); err != nil {
					return
				}
			}
			if slot < next {
				continue // more frames than the grid: drop
			}
			if _, err := vw.Write(f.data); err != nil {
				return
			}
			rec.frames.Add(1)
			last = f.data
			next++
		}
	}()

	if withAudio {
		rec.wg.Add(1)
		go func() { // audio: sample-count timeline aligned to t0
			defer rec.wg.Done()
			aw, err := in.audio()
			if err != nil {
				slog.Error("recording audio input", "error", err)
				return
			}
			defer aw.Close()
			var t0 time.Time
			select {
			case <-rec.stop:
				return
			case t0 = <-t0ch:
			}
			const frameBytes = 2 * audioChannels
			chunkFrames := int64(audioChunk / frameBytes)
			tolerance := int64(audioRate / 20) // 50 ms
			var written int64                  // sample frames written so far
			silence := make([]byte, audioChunk)
			for c := range chunks {
				if c.at.Before(t0) {
					continue
				}
				// position of this chunk's first sample on the video clock
				want := int64(c.at.Sub(t0).Seconds()*audioRate) - chunkFrames
				if written-want > tolerance {
					continue // audio ahead of video clock: drop chunk
				}
				if gap := want - written; gap > tolerance {
					// behind the video clock: fill the whole gap with silence
					for gap > 0 {
						n := min(gap, chunkFrames)
						if _, err := aw.Write(silence[:n*frameBytes]); err != nil {
							return
						}
						written += n
						gap -= n
					}
				}
				if _, err := aw.Write(c.data); err != nil {
					return
				}
				written += chunkFrames
			}
		}()
	}

	go func() {
		err := rec.cmd.Wait()
		close(rec.exited)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.active == rec {
			// ffmpeg ended without stop(): report it
			r.active = nil
			close(rec.stop)
			r.lastErr = fmt.Sprintf("ffmpeg exited: %v %s", err, strings.TrimSpace(rec.stderr.String()))
			slog.Error("recording failed", "file", rec.path, "error", err, "ffmpeg", lastLine(strings.TrimSpace(rec.stderr.String())))
		}
	}()

	r.active = rec
	r.lastErr = ""
	slog.Info("recording started", "file", rec.path, "format", format, "codec", codecNames[enc.Codec],
		"encoder", enc.Name, "gpu", enc.GPU, "quality", quality, "fps", fps)
	return name, nil
}

func (r *recorder) stopRecording() error {
	r.mu.Lock()
	rec := r.active
	r.active = nil
	r.mu.Unlock()
	if rec == nil {
		return errors.New("not recording")
	}
	close(rec.stop)
	// The feeders close the pipes, ffmpeg sees EOF and finalizes the file.
	// If ffmpeg hangs, a feeder can block in Write: killing ffmpeg after a
	// timeout breaks the pipe and lets everything finish.
	fed := make(chan struct{})
	go func() { rec.wg.Wait(); close(fed) }()
	deadline := time.After(30 * time.Second)
	select {
	case <-rec.exited:
	case <-deadline:
		slog.Warn("ffmpeg did not finish, killing it", "file", rec.path)
		rec.cmd.Process.Kill()
		<-rec.exited
	}
	<-fed
	ffErr := strings.TrimSpace(rec.stderr.String())
	if ffErr != "" {
		slog.Warn("ffmpeg reported errors", "file", rec.path, "stderr", ffErr)
	}
	if rec.frames.Load() == 0 {
		os.Remove(rec.path)
		err := errors.New("no video was received, nothing saved (is the capture device in use by another program?)")
		r.setErr(err.Error())
		return err
	}
	if fi, err := os.Stat(rec.path); err != nil || fi.Size() == 0 {
		os.Remove(rec.path)
		err := fmt.Errorf("ffmpeg produced no output: %s", lastLine(ffErr))
		r.setErr(err.Error())
		return err
	}
	slog.Info("recording saved", "file", rec.path, "duration", time.Since(rec.started).Round(time.Second), "frames", rec.frames.Load())
	return nil
}

func (r *recorder) setErr(msg string) {
	r.mu.Lock()
	r.lastErr = msg
	r.mu.Unlock()
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (r *recorder) status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	formats := map[string]string{}
	for k, f := range recordFormats {
		formats[k] = f.Label
	}
	st := map[string]any{"available": r.ffmpeg != "", "active": r.active != nil, "dir": r.store.Dir(),
		"error": r.lastErr, "formats": formats, "audio": r.audio != nil}
	if encs, ready := r.encoder.all(); ready {
		st["encoders"] = encs
	}
	if rec := r.active; rec != nil {
		st["file"] = rec.name
		st["format"] = rec.format
		st["encoder"] = rec.encoder
		st["recordingAudio"] = rec.audio
		st["seconds"] = int(time.Since(rec.started).Seconds())
		st["frames"] = rec.frames.Load()
		if fi, err := os.Stat(rec.path); err == nil {
			st["bytes"] = fi.Size()
		}
	}
	return st
}

func (r *recorder) isActive(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active != nil && r.active.path == filepath.Join(r.store.Dir(), name)
}

// tailBuffer keeps the last max bytes written (ffmpeg stderr).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
