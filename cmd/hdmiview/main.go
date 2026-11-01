// hdmiview serves a USB HDMI capture device (MS2109) to a web browser:
// MJPEG video as multipart/x-mixed-replace and raw PCM audio played with
// Web Audio. The capture device is opened only while someone is watching.
// Video mode and picture controls can be changed from the page.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jkandasa/hdmiview/internal/cli"
)

//go:embed index.html
var indexHTML []byte

// hub fans data out to subscribers. Video subscribers only want the latest
// frame; audio subscribers get a short queue and drop on overflow.
type hub struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	queue   int
	wake    chan struct{}
	counter atomic.Uint64
	bytes   atomic.Uint64
}

func newHub(queue int) *hub {
	return &hub{subs: map[chan []byte]struct{}{}, queue: queue, wake: make(chan struct{}, 1)}
}

func (h *hub) subscribe() chan []byte { return h.subscribeN(h.queue) }

// subscribeN subscribes with a custom queue length (e.g. a recorder that
// must not skip frames while the encoder catches up).
func (h *hub) subscribeN(n int) chan []byte {
	ch := make(chan []byte, n)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *hub) publish(data []byte) {
	h.counter.Add(1)
	h.bytes.Add(uint64(len(data)))
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- data:
		default:
			if h.queue == 1 { // replace stale frame with the newest one
				select {
				case <-ch:
				default:
				}
				ch <- data
			}
		}
	}
}

// waitActive blocks until there is at least one subscriber.
func (h *hub) waitActive() {
	for h.count() == 0 {
		<-h.wake
	}
}

var retryInterval = time.Second // variable for tests

// runWhileWatched runs the source while there are subscribers, retrying
// after errors (e.g. device unplugged or busy). run must return once stop
// closes; it may also return nil early to be restarted (e.g. settings
// changed). run calls started once data actually flows.
//
// Logging stays quiet while retrying: a failure is logged once (again only
// if the error changes), and recovery is logged as "device found".
func runWhileWatched(name string, h *hub, run func(stop <-chan struct{}, started func(attrs ...any)) error) {
	var failing bool
	var failSince time.Time
	var lastErr string
	for {
		h.waitActive()
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					if h.count() == 0 {
						close(stop)
						return
					}
				}
			}
		}()
		var once sync.Once
		started := func(attrs ...any) {
			once.Do(func() {
				if failing {
					attrs = append(attrs, "unavailableFor", time.Since(failSince).Round(time.Second).String())
					announce(name+" device found", attrs...)
					failing, lastErr = false, ""
				} else {
					slog.Info(name+" capture started", attrs...)
				}
			})
		}
		err := run(stop, started)
		close(done)
		switch {
		case err != nil:
			if msg := err.Error(); !failing || msg != lastErr {
				if !failing {
					failSince = time.Now()
				}
				slog.Warn(name+" device not available, retrying", "error", err, "retryEvery", retryInterval.String())
				failing, lastErr = true, msg
			} else {
				slog.Debug(name+" device still not available", "error", err)
			}
			time.Sleep(retryInterval)
		case h.count() == 0:
			slog.Info("no viewers, device released", "source", name)
		}
	}
}

// guard protects state-changing requests from other web sites: a page in
// the user's browser could otherwise POST to the localhost server.
// Requests must come from this origin and POSTs must be JSON, which a
// cross-site page cannot send without a CORS preflight (never granted).
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
			if r.Method == http.MethodPost {
				if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
					http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address (use 0.0.0.0:8080 for LAN access)")
	match := flag.String("match", "MS2109", "device name to look for")
	videoDev := flag.String("video", "", "video device: V4L2 path on Linux, device name on Windows/macOS (default: auto-detect)")
	audioDev := flag.String("audio", "", "audio device: ALSA name on Linux, device name on Windows/macOS (default: auto-detect, \"none\" to disable)")
	format := flag.String("format", "MJPG", "initial video format: MJPG or YUYV")
	width := flag.Int("width", 1920, "initial capture width")
	height := flag.Int("height", 1080, "initial capture height")
	fps := flag.Int("fps", 30, "initial capture frame rate")
	quality := flag.Int("quality", 90, "JPEG quality for YUYV")
	settingsPath := flag.String("settings", defaultSettingsPath(), "file to remember the video mode (\"\" = don't save)")
	recordDir := flag.String("recordings", "", "folder for recordings (default: saved setting or ~/Videos/hdmiview)")
	snapDir := flag.String("snapshots", "", "folder for snapshots (default: saved setting or ~/Pictures/hdmiview)")
	logLevel := flag.String("log-level", "warning", "log level: debug, info, warning (or warn), error")
	logFormat := flag.String("log-format", "console", "log format: console (zap development style) or json (zap production style)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: hdmiview [flags]\n\nServes a USB HDMI capture device (MS2109) to a web browser.\n\nflags:\n")
		cli.PrintDefaults(os.Stderr, flag.CommandLine)
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(cli.VersionString("hdmiview"))
		return
	}
	if err := setupLogging(*logLevel, *logFormat); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	initial := videoMode{Format: *format, Width: *width, Height: *height, FPS: *fps, Quality: *quality}
	st := loadSettings(*settingsPath, initial, folders{Recordings: defaultRecordingsDir(), Snapshots: defaultSnapshotsDir()})
	modeFlagSet := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "format", "width", "height", "fps", "quality":
			modeFlagSet = true
		}
	})
	if modeFlagSet { // explicit flags win over the saved mode
		st.set(initial)
	}
	if *recordDir != "" || *snapDir != "" { // explicit flags win over saved folders
		f := st.getFolders()
		for _, x := range []struct {
			flag string
			dst  *string
		}{{*recordDir, &f.Recordings}, {*snapDir, &f.Snapshots}} {
			if x.flag == "" {
				continue
			}
			dir, err := prepareDir(x.flag)
			if err != nil {
				fatal("invalid folder", "path", x.flag, "error", err)
			}
			*x.dst = dir
		}
		st.setFolders(f)
	}
	dirs := st.getFolders()
	recStore := newFileStore(dirs.Recordings, ".mp4", ".mkv")
	snapStore := newFileStore(dirs.Snapshots, ".jpg")

	be, err := newBackend(*videoDev, *audioDev, *match)
	if err != nil {
		fatal("capture backend", "error", err)
	}
	devicePath := be.videoDevice

	video := newHub(1)
	audio := newHub(50) // 0.5 s of 10 ms chunks
	var recAudio *hub
	if *audioDev != "none" {
		recAudio = audio
	}
	rec := newRecorder(recStore, video, recAudio)

	go runWhileWatched("video", video, func(stop <-chan struct{}, started func(attrs ...any)) error {
		dev, err := devicePath()
		if err != nil {
			return err
		}
		mode, changed := st.get()
		if modes, err := be.listModes(dev); err == nil {
			if fit := fitMode(mode, modes); fit != mode {
				slog.Warn("video mode not supported, using closest", "requested", mode, "using", fit)
				mode = fit
			}
		}
		v, err := be.openVideo(dev, mode)
		if err != nil {
			return err
		}
		defer v.close()
		for {
			select {
			case <-stop:
				return nil
			case <-changed:
				return nil // reopen with the new mode
			default:
			}
			frame, err := v.next()
			if err != nil {
				return err
			}
			if frame != nil {
				started("device", dev, "format", mode.Format, "width", mode.Width, "height", mode.Height, "fps", mode.FPS)
				video.publish(frame)
			}
		}
	})

	if *audioDev != "none" {
		go runWhileWatched("audio", audio, func(stop <-chan struct{}, started func(attrs ...any)) error {
			dev, err := be.audioDevice()
			if err != nil {
				return err
			}
			// report "started" once the first audio chunk arrives
			before := audio.counter.Load()
			watching := make(chan struct{})
			defer close(watching)
			go func() {
				t := time.NewTicker(100 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-watching:
						return
					case <-t.C:
						if audio.counter.Load() > before {
							started("device", dev, "rate", audioRate, "channels", audioChannels)
							return
						}
					}
				}
			}()
			return be.runAudio(dev, audio, stop)
		})
	}

	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	http.HandleFunc("GET /video", func(w http.ResponseWriter, r *http.Request) {
		const boundary = "hdmiviewframe"
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
		w.Header().Set("Cache-Control", "no-store")
		ch := video.subscribe()
		defer video.unsubscribe(ch)
		rc := http.NewResponseController(w)
		for {
			select {
			case <-r.Context().Done():
				return
			case frame := <-ch:
				fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(frame))
				w.Write(frame)
				w.Write([]byte("\r\n"))
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	})

	// grabFrame returns the next video frame (starting capture if needed).
	grabFrame := func() ([]byte, error) {
		ch := video.subscribe()
		defer video.unsubscribe(ch)
		select {
		case frame := <-ch:
			return frame, nil
		case <-time.After(5 * time.Second):
			return nil, errors.New("no frame from capture device")
		}
	}

	http.HandleFunc("GET /snapshot.jpg", func(w http.ResponseWriter, r *http.Request) {
		frame, err := grabFrame()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Disposition", "inline; filename=hdmi-"+time.Now().Format("20060102-150405")+".jpg")
		w.Write(frame)
	})

	// Save a snapshot into the snapshots folder.
	http.HandleFunc("POST /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		frame, err := grabFrame()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		dir := snapStore.Dir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := "hdmi-" + time.Now().Format("20060102-150405.000") + ".jpg"
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, err = f.Write(frame)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("snapshot saved", "file", filepath.Join(dir, name), "bytes", len(frame))
		writeJSON(w, map[string]any{"name": name, "dir": dir, "bytes": len(frame)})
	})

	http.HandleFunc("GET /api/snapshots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"dir": snapStore.Dir(), "snapshots": snapStore.List()})
	})

	// --- folders API ---

	http.HandleFunc("GET /api/folders", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, st.getFolders())
	})

	http.HandleFunc("POST /api/folders", func(w http.ResponseWriter, r *http.Request) {
		var req folders
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f := st.getFolders()
		for _, x := range []struct {
			name, value string
			dst         *string
			store       *fileStore
		}{{"recordings", req.Recordings, &f.Recordings, recStore}, {"snapshots", req.Snapshots, &f.Snapshots, snapStore}} {
			if x.value == "" {
				continue
			}
			dir, err := prepareDir(x.value)
			if err != nil {
				http.Error(w, x.name+" folder: "+err.Error(), http.StatusBadRequest)
				return
			}
			*x.dst = dir
			x.store.SetDir(dir)
			slog.Info("folder changed", "kind", x.name, "path", dir)
		}
		if err := st.setFolders(f); err != nil {
			slog.Error("saving settings failed", "error", err)
		}
		writeJSON(w, f)
	})

	http.HandleFunc("GET /audio", func(w http.ResponseWriter, r *http.Request) {
		if *audioDev == "none" {
			http.Error(w, "audio disabled", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Audio-Format", fmt.Sprintf("s16le;rate=%d;channels=%d", audioRate, audioChannels))
		ch := audio.subscribe()
		defer audio.unsubscribe(ch)
		rc := http.NewResponseController(w)
		for {
			select {
			case <-r.Context().Done():
				return
			case chunk := <-ch:
				if _, err := w.Write(chunk); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	})

	// --- settings API ---

	http.HandleFunc("GET /api/video", func(w http.ResponseWriter, r *http.Request) {
		dev, err := devicePath()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		modes, err := be.listModes(dev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		mode, _ := st.get()
		writeJSON(w, map[string]any{"device": dev, "mode": mode, "modes": modes})
	})

	http.HandleFunc("POST /api/video", func(w http.ResponseWriter, r *http.Request) {
		var m videoMode
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m.Quality == 0 {
			m.Quality = *quality
		}
		dev, err := devicePath()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		modes, err := be.listModes(dev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := validateMode(m, modes); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if rec.status()["active"] == true {
			http.Error(w, "stop the recording before changing the video mode", http.StatusConflict)
			return
		}
		if err := st.set(m); err != nil {
			slog.Error("saving settings failed", "error", err)
		}
		slog.Info("video mode changed", "format", m.Format, "width", m.Width, "height", m.Height, "fps", m.FPS, "quality", m.Quality)
		writeJSON(w, m)
	})

	http.HandleFunc("GET /api/controls", func(w http.ResponseWriter, r *http.Request) {
		dev, err := devicePath()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		ctrls, err := be.listControls(dev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, ctrls)
	})

	http.HandleFunc("POST /api/controls", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID    uint32 `json:"id"`
			Value *int32 `json:"value"`
			Reset bool   `json:"reset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dev, err := devicePath()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		ctrls, err := be.listControls(dev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		for _, c := range ctrls {
			if c.ID != req.ID {
				continue
			}
			value := c.Default
			if !req.Reset {
				if req.Value == nil {
					http.Error(w, "value or reset required", http.StatusBadRequest)
					return
				}
				value = *req.Value
			}
			if c.ReadOnly || value < c.Min || value > c.Max {
				http.Error(w, fmt.Sprintf("%s: value %d not allowed (%d..%d)", c.Name, value, c.Min, c.Max), http.StatusBadRequest)
				return
			}
			got, err := be.setControl(dev, c.ID, value)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			c.Value = got
			writeJSON(w, c)
			return
		}
		http.Error(w, "unknown control", http.StatusNotFound)
	})

	// --- recording API ---

	http.HandleFunc("GET /api/record", func(w http.ResponseWriter, r *http.Request) {
		st := rec.status()
		st["recordings"] = recStore.List()
		writeJSON(w, st)
	})

	http.HandleFunc("POST /api/record", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action  string `json:"action"`
			Format  string `json:"format"`
			Quality string `json:"quality"`
			Codec   string `json:"codec"`
			Encoder string `json:"encoder"` // auto (default), hardware or software
			Audio   *bool  `json:"audio"`   // default true
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var err error
		switch req.Action {
		case "start":
			mode, _ := st.get()
			_, err = rec.start(req.Format, req.Quality, req.Codec, req.Encoder, req.Audio == nil || *req.Audio, mode.FPS)
		case "stop":
			err = rec.stopRecording()
		default:
			err = fmt.Errorf("unknown action %q", req.Action)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, rec.status())
	})

	// Files in the recordings/snapshots folders: GET serves (?download=1
	// forces a download), DELETE removes. Only the stores' own names work.
	serveFiles := func(prefix string, store *fileStore, busy func(name string) bool) {
		http.HandleFunc("GET "+prefix+"{name}", func(w http.ResponseWriter, r *http.Request) {
			path, ok := store.Path(r.PathValue("name"))
			if !ok {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("download") != "" {
				w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
			}
			http.ServeFile(w, r, path)
		})
		http.HandleFunc("DELETE "+prefix+"{name}", func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			path, ok := store.Path(name)
			if !ok {
				http.NotFound(w, r)
				return
			}
			if busy != nil && busy(name) {
				http.Error(w, "file is in use (stop the recording first)", http.StatusConflict)
				return
			}
			if err := os.Remove(path); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			slog.Info("file deleted", "file", path)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	serveFiles("/recordings/", recStore, rec.isActive)
	serveFiles("/snapshots/", snapStore, nil)

	var lastFrames, lastBytes, lastChunks uint64
	var fpsNow, kbpsNow atomic.Value
	var audioLive atomic.Bool
	fpsNow.Store(0.0)
	kbpsNow.Store(0.0)
	go func() {
		for range time.Tick(time.Second) {
			f, b, a := video.counter.Load(), video.bytes.Load(), audio.counter.Load()
			fpsNow.Store(float64(f - lastFrames))
			kbpsNow.Store(float64(b-lastBytes) * 8 / 1000)
			audioLive.Store(a > lastChunks)
			lastFrames, lastBytes, lastChunks = f, b, a
		}
	}()
	http.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		mode, _ := st.get()
		writeJSON(w, map[string]any{
			"fps":          fpsNow.Load(),
			"kbps":         kbpsNow.Load(),
			"audio":        audioLive.Load(),
			"videoClients": video.count(),
			"audioClients": audio.count(),
			"mode":         mode,
			"recording":    rec.status()["active"],
		})
	})

	// Finish an active recording cleanly on Ctrl+C / SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		if rec.status()["active"] == true {
			slog.Info("stopping recording before exit")
			rec.stopRecording()
		}
		os.Exit(0)
	}()

	announce("hdmiview listening", "version", cli.ShortVersion(), "url", "http://"+*listen+"/", "recordings", recStore.Dir(), "snapshots", snapStore.Dir())
	srv := &http.Server{
		Addr:              *listen,
		Handler:           guard(http.DefaultServeMux),
		ReadHeaderTimeout: 10 * time.Second, // no write timeout: /video and /audio stream forever
	}
	if err := srv.ListenAndServe(); err != nil {
		fatal("http server stopped", "error", err)
	}
}
