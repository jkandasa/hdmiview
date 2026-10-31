package main

// Capture backends. Linux talks to V4L2 and ALSA directly (backend_linux.go);
// other systems use ffmpeg for capture and the chip's HID interface for
// picture controls (backend_ffmpeg.go).

// Audio format delivered by every backend. The patched firmware offers it
// natively; with stock firmware (96 kHz mono) ffmpeg or ALSA plughw converts.
const (
	audioRate     = 48000
	audioChannels = 2
	// 10 ms of S16_LE stereo per chunk
	audioChunk = audioRate / 100 * audioChannels * 2
)

// videoStream delivers complete JPEG frames; next returns nil for frames
// that should be skipped.
type videoStream interface {
	next() ([]byte, error)
	close()
}

type captureBackend interface {
	// videoDevice resolves the video device to use (path or name).
	videoDevice() (string, error)
	listModes(dev string) ([]videoModeInfo, error)
	openVideo(dev string, mode videoMode) (videoStream, error)
	listControls(dev string) ([]videoControl, error)
	setControl(dev string, id uint32, value int32) (int32, error)
	// audioDevice resolves the audio device to use.
	audioDevice() (string, error)
	// runAudio streams S16LE stereo 48 kHz PCM into h until stop closes
	// (returns nil) or capture fails.
	runAudio(dev string, h *hub, stop <-chan struct{}) error
}

type videoModeInfo struct {
	Format string `json:"format"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	FPS    []int  `json:"fps"`
}

type videoControl struct {
	ID       uint32         `json:"id"`
	Name     string         `json:"name"`
	Type     string         `json:"type"` // int, bool, menu
	Min      int32          `json:"min"`
	Max      int32          `json:"max"`
	Step     int32          `json:"step"`
	Default  int32          `json:"default"`
	Value    int32          `json:"value"`
	ReadOnly bool           `json:"readOnly"`
	Menu     map[int]string `json:"menu,omitempty"`
}

type videoMode struct {
	Format  string `json:"format"` // MJPG or YUYV
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	FPS     int    `json:"fps"`
	Quality int    `json:"quality"` // JPEG quality for YUYV
}
