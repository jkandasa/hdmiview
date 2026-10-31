package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// zapConsoleHandler is a slog.Handler that mimics zap's development console
// encoder:
//
//	2026-10-31T22:10:00.123+0530	INFO	hdmiview/main.go:245	video opened	{"device": "/dev/video6", "fps": 30}
type zapConsoleHandler struct {
	mu     *sync.Mutex
	w      io.Writer
	level  slog.Leveler
	color  bool
	attrs  []slog.Attr // pre-bound via WithAttrs, already group-qualified
	groups []string
}

func newZapConsoleHandler(w io.Writer, level slog.Leveler) *zapConsoleHandler {
	return &zapConsoleHandler{mu: &sync.Mutex{}, w: w, level: level, color: isTerminal(w)}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (h *zapConsoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *zapConsoleHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), qualify(h.groups, as)...)
	return &c
}

func (h *zapConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.groups = append(append([]string{}, h.groups...), name)
	return &c
}

// qualify nests attrs inside the open groups.
func qualify(groups []string, as []slog.Attr) []slog.Attr {
	if len(groups) == 0 || len(as) == 0 {
		return as
	}
	args := make([]any, len(as))
	for i, a := range as {
		args[i] = a
	}
	a := slog.Group(groups[len(groups)-1], args...)
	return qualify(groups[:len(groups)-1], []slog.Attr{a})
}

var levelColors = map[slog.Level]string{
	slog.LevelDebug: "\x1b[35m", // magenta
	slog.LevelInfo:  "\x1b[34m", // blue
	slog.LevelWarn:  "\x1b[33m", // yellow
	slog.LevelError: "\x1b[31m", // red
}

func (h *zapConsoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	b.WriteString(t.Format("2006-01-02T15:04:05.000Z0700"))
	b.WriteByte('\t')

	lvl := r.Level.String()
	if c, ok := levelColors[r.Level]; ok && h.color {
		lvl = c + lvl + "\x1b[0m"
	}
	b.WriteString(lvl)
	b.WriteByte('\t')

	if r.PC != 0 {
		fr, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		b.WriteString(shortCaller(fr.File))
		fmt.Fprintf(&b, ":%d\t", fr.Line)
	}
	b.WriteString(r.Message)

	attrs := append([]slog.Attr{}, h.attrs...)
	var recAttrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		recAttrs = append(recAttrs, a)
		return true
	})
	attrs = append(attrs, qualify(h.groups, recAttrs)...)
	if len(attrs) > 0 {
		b.WriteByte('\t')
		writeFields(&b, attrs)
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b.Bytes())
	return err
}

// shortCaller returns "dir/file.go" like zap's ShortCallerEncoder.
func shortCaller(file string) string {
	dir, f := filepath.Split(file)
	return filepath.Base(dir) + "/" + f
}

// writeFields writes attrs as zap does: {"key": value, "key2": value2}.
func writeFields(b *bytes.Buffer, attrs []slog.Attr) {
	b.WriteByte('{')
	first := true
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Equal(slog.Attr{}) {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		key, _ := json.Marshal(a.Key)
		b.Write(key)
		b.WriteString(": ")
		writeValue(b, a.Value)
	}
	b.WriteByte('}')
}

func writeValue(b *bytes.Buffer, v slog.Value) {
	switch v.Kind() {
	case slog.KindGroup:
		writeFields(b, v.Group())
		return
	case slog.KindDuration:
		v = slog.StringValue(v.Duration().String())
	case slog.KindTime:
		v = slog.StringValue(v.Time().Format(time.RFC3339Nano))
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			v = slog.StringValue(x.Error())
		case fmt.Stringer:
			v = slog.StringValue(x.String())
		}
	}
	data, err := json.Marshal(v.Any())
	if err != nil {
		data, _ = json.Marshal(fmt.Sprint(v.Any()))
	}
	b.Write(data)
}

// setupLogging installs the default logger. format is "console" (zap
// development style) or "json" (zap production style keys).
func setupLogging(level, format string) error {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("log level %q: use debug, info, warn or error", level)
	}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "console", "":
		h = newZapConsoleHandler(os.Stderr, lvl)
	case "json":
		h = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level:     lvl,
			AddSource: true,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if len(groups) > 0 {
					return a
				}
				switch a.Key { // zap production key names
				case slog.TimeKey:
					a.Key = "ts"
				case slog.LevelKey:
					a.Key = "level"
					a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
				case slog.MessageKey:
					a.Key = "msg"
				case slog.SourceKey:
					if src, ok := a.Value.Any().(*slog.Source); ok {
						return slog.String("caller", fmt.Sprintf("%s:%d", shortCaller(src.File), src.Line))
					}
				}
				return a
			},
		})
	default:
		return fmt.Errorf("log format %q: use console or json", format)
	}
	slog.SetDefault(slog.New(h))
	return nil
}

// fatal logs at error level and exits, like zap's Fatal.
func fatal(msg string, args ...any) {
	logAt(slog.LevelError, msg, args...)
	os.Exit(1)
}

// logAt logs with the caller of logAt's caller as source, so helper
// wrappers (fatal) report the right file:line.
func logAt(level slog.Level, msg string, args ...any) {
	l := slog.Default()
	if !l.Enabled(context.Background(), level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(args...)
	l.Handler().Handle(context.Background(), r)
}
