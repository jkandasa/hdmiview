package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreSafety(t *testing.T) {
	dir := t.TempDir()
	s := newFileStore(dir, ".jpg")
	os.WriteFile(filepath.Join(dir, "hdmi-ok.jpg"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.jpg"), []byte("x"), 0o644)
	os.Symlink("/etc/passwd", filepath.Join(dir, "hdmi-link.jpg"))
	os.Mkdir(filepath.Join(dir, "hdmi-dir.jpg"), 0o755)

	for name, want := range map[string]bool{
		"hdmi-ok.jpg":      true,
		"other.jpg":        false, // not ours
		"hdmi-link.jpg":    false, // symlink
		"hdmi-dir.jpg":     false, // directory
		"hdmi-missing.jpg": false,
		"hdmi-ok.mp4":      false, // wrong extension
		"../hdmi-ok.jpg":   false,
		"hdmi-..%2Fx.jpg":  false,
		`hdmi-a\b.jpg`:     false,
	} {
		if _, ok := s.Path(name); ok != want {
			t.Errorf("Path(%q) = %v, want %v", name, ok, want)
		}
	}
	if l := s.List(); len(l) != 1 || l[0].Name != "hdmi-ok.jpg" {
		t.Errorf("List() = %+v", l)
	}
}

func TestPrepareDir(t *testing.T) {
	if _, err := prepareDir("relative/dir"); err == nil {
		t.Error("relative path accepted")
	}
	if _, err := prepareDir("  "); err == nil {
		t.Error("empty path accepted")
	}
	d := filepath.Join(t.TempDir(), "a", "b")
	got, err := prepareDir(d + "/")
	if err != nil || got != d {
		t.Fatalf("prepareDir = %q, %v", got, err)
	}
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		t.Error("directory not created")
	}
	home, _ := os.UserHomeDir()
	if got, err := prepareDir("~"); err != nil || got != home {
		t.Errorf("~ -> %q, %v", got, err)
	}
}
