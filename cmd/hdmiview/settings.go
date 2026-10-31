package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// settings holds the active video mode, persists it, and notifies the
// capture loop when it changes.
type settings struct {
	mu      sync.Mutex
	path    string
	video   videoMode
	folders folders
	changed chan struct{}
}

type folders struct {
	Recordings string `json:"recordings"`
	Snapshots  string `json:"snapshots"`
}

type savedSettings struct {
	Video   videoMode `json:"video"`
	Folders folders   `json:"folders"`
}

func defaultSettingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "hdmiview", "settings.json")
}

func loadSettings(path string, fallback videoMode, fallbackFolders folders) *settings {
	s := &settings{path: path, video: fallback, folders: fallbackFolders, changed: make(chan struct{})}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var saved savedSettings
	if err := json.Unmarshal(data, &saved); err != nil {
		slog.Warn("ignoring unreadable settings file", "path", path, "error", err)
		return s
	}
	if saved.Video.Format != "" {
		s.video = saved.Video
	}
	if saved.Folders.Recordings != "" {
		s.folders.Recordings = saved.Folders.Recordings
	}
	if saved.Folders.Snapshots != "" {
		s.folders.Snapshots = saved.Folders.Snapshots
	}
	return s
}

func (s *settings) get() (videoMode, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.video, s.changed
}

func (s *settings) set(m videoMode) error {
	s.mu.Lock()
	s.video = m
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return s.save()
}

func (s *settings) getFolders() folders {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.folders
}

func (s *settings) setFolders(f folders) error {
	s.mu.Lock()
	s.folders = f
	s.mu.Unlock()
	return s.save()
}

func (s *settings) save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	data, _ := json.MarshalIndent(savedSettings{Video: s.video, Folders: s.folders}, "", "  ")
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, append(data, '\n'), 0o644)
}

// validateMode checks m against the modes the device reports.
func validateMode(m videoMode, modes []videoModeInfo) error {
	if m.Format != "MJPG" && (m.Quality < 10 || m.Quality > 100) { // server-side JPEG encoding
		return fmt.Errorf("quality must be 10..100")
	}
	for _, mi := range modes {
		if mi.Format == m.Format && mi.Width == m.Width && mi.Height == m.Height {
			if slices.Contains(mi.FPS, m.FPS) {
				return nil
			}
			return fmt.Errorf("%s %dx%d supports %v fps, not %d", m.Format, m.Width, m.Height, mi.FPS, m.FPS)
		}
	}
	return fmt.Errorf("device does not support %s %dx%d", m.Format, m.Width, m.Height)
}

// fitMode returns m if the device supports it, otherwise the closest usable
// mode: same format and size at its highest rate, else the first mode.
func fitMode(m videoMode, modes []videoModeInfo) videoMode {
	if validateMode(m, modes) == nil {
		return m
	}
	for _, mi := range modes {
		if mi.Format == m.Format && mi.Width == m.Width && mi.Height == m.Height && len(mi.FPS) > 0 {
			m.FPS = slices.Max(mi.FPS)
			return m
		}
	}
	mi := modes[0]
	return videoMode{Format: mi.Format, Width: mi.Width, Height: mi.Height, FPS: slices.Max(mi.FPS), Quality: m.Quality}
}
