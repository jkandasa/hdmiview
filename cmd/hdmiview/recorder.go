package main

import (
	"errors"
	"fmt"
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
	args  func(quality string) []string
}

var recordFormats = map[string]recordFormat{
	"mp4": {".mp4", "MP4 (H.264, small)", func(q string) []string {
		crf := map[string]string{"high": "18", "normal": "23", "small": "28"}[q]
		if crf == "" {
			crf = "23"
		}
		// -bf 0: B-frames delay the first video timestamp by two frames,
		// which fragmented MP4 cannot express (audio would lead by ~67 ms).
		return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", crf, "-bf", "0", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "192k",
			// fragmented MP4 stays playable if recording is interrupted
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof"}
	}},
	"mkv": {".mkv", "MKV (original MJPEG, large)", func(string) []string {
		return []string{"-c:v", "copy", "-c:a", "pcm_s16le"}
	}},
}

type recording struct {
	name, path, format, quality string
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
	active  *recording
	lastErr string
}

func newRecorder(store *fileStore, video, audio *hub) *recorder {
	ff, _ := exec.LookPath("ffmpeg")
	return &recorder{store: store, ffmpeg: ff, video: video, audio: audio}
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

func (r *recorder) start(format, quality string, fps int) (string, error) {
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
	rec := &recording{name: name, path: filepath.Join(dir, name), format: format, quality: quality,
		started: time.Now(), stop: make(chan struct{}), exited: make(chan struct{}), stderr: &tailBuffer{max: 4096}}

	vr, vw, err := os.Pipe()
	if err != nil {
		return "", err
	}
	files := []*os.File{vr}
	if fps <= 0 {
		fps = 30
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-thread_queue_size", "1024", "-f", "mjpeg", "-framerate", fmt.Sprint(fps), "-i", "pipe:3"}
	var ar, aw *os.File
	if r.audio != nil {
		if ar, aw, err = os.Pipe(); err != nil {
			vr.Close()
			vw.Close()
			return "", err
		}
		files = append(files, ar)
		args = append(args, "-thread_queue_size", "1024", "-f", "s16le",
			"-ar", fmt.Sprint(audioRate), "-ac", fmt.Sprint(audioChannels), "-i", "pipe:4",
			"-map", "0:v", "-map", "1:a")
	}
	args = append(args, "-fps_mode", "cfr")
	args = append(args, f.args(quality)...)
	args = append(args, rec.path)

	rec.cmd = exec.Command(r.ffmpeg, args...)
	rec.cmd.ExtraFiles = files
	rec.cmd.Stderr = rec.stderr
	err = rec.cmd.Start()
	vr.Close() // child holds its own copies
	if ar != nil {
		ar.Close()
	}
	if err != nil {
		vw.Close()
		if aw != nil {
			aw.Close()
		}
		return "", err
	}

	// Both feeders measure time from t0, the arrival of the first frame.
	t0ch := make(chan time.Time, 1)
	frames := stampedFeed(r.video, 60, rec.stop)
	var chunks <-chan stamped
	if aw != nil {
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

	if aw != nil {
		rec.wg.Add(1)
		go func() { // audio: sample-count timeline aligned to t0
			defer rec.wg.Done()
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
	slog.Info("recording started", "file", rec.path, "format", format, "quality", quality, "fps", fps)
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
	if rec := r.active; rec != nil {
		st["file"] = rec.name
		st["format"] = rec.format
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
