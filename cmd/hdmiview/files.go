package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// fileStore is a directory of files created by this program. Only names of
// the form "hdmi-*<ext>" are listed, served or deleted, so a configurable
// directory can never expose or remove anything else.
type fileStore struct {
	mu   sync.Mutex
	dir  string
	exts []string
}

type fileInfo struct {
	Name    string    `json:"name"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"time"`
}

func newFileStore(dir string, exts ...string) *fileStore {
	return &fileStore{dir: dir, exts: exts}
}

func (f *fileStore) Dir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dir
}

func (f *fileStore) SetDir(dir string) {
	f.mu.Lock()
	f.dir = dir
	f.mu.Unlock()
}

func (f *fileStore) Valid(name string) bool {
	if !strings.HasPrefix(name, "hdmi-") || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, e := range f.exts {
		if strings.HasSuffix(name, e) {
			return true
		}
	}
	return false
}

// Path returns the full path of a valid name in the current directory. The
// file must be a regular file: symlinks are refused so a link planted in the
// folder cannot expose (or be used to remove) anything outside it.
func (f *fileStore) Path(name string) (string, bool) {
	if !f.Valid(name) {
		return "", false
	}
	p := filepath.Join(f.Dir(), name)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

func (f *fileStore) List() []fileInfo {
	entries, _ := os.ReadDir(f.Dir())
	out := []fileInfo{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !f.Valid(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, fileInfo{e.Name(), fi.Size(), fi.ModTime()})
		}
	}
	slices.SortFunc(out, func(a, b fileInfo) int { return b.ModTime.Compare(a.ModTime) })
	return out
}

// prepareDir expands "~", requires an absolute path, creates the directory
// and checks that it is writable. It returns the cleaned path.
func prepareDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("folder must not be empty")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	p = filepath.Clean(p)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(p, ".hdmiview-write-test-*")
	if err != nil {
		return "", fmt.Errorf("folder is not writable: %w", err)
	}
	tmp.Close()
	os.Remove(tmp.Name())
	return p, nil
}

func defaultRecordingsDir() string {
	if runtime.GOOS == "darwin" {
		return homeSubdir("Movies", "hdmiview")
	}
	return homeSubdir("Videos", "hdmiview")
}
func defaultSnapshotsDir() string { return homeSubdir("Pictures", "hdmiview") }

func homeSubdir(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(append([]string{"."}, parts...)...)
	}
	return filepath.Join(append([]string{home}, parts...)...)
}
