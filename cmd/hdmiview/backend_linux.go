package main

// v4l2Backend captures through V4L2 (video, controls) and ALSA (audio).
type v4l2Backend struct {
	video, audio, match string // --video, --audio, --match
}

func newBackend(video, audio, match string) (captureBackend, error) {
	return &v4l2Backend{video: video, audio: audio, match: match}, nil
}

func (b *v4l2Backend) videoDevice() (string, error) {
	if b.video != "" {
		return b.video, nil
	}
	return findVideoDevice(b.match)
}

func (b *v4l2Backend) listModes(dev string) ([]videoModeInfo, error) { return listModes(dev) }

func (b *v4l2Backend) openVideo(dev string, mode videoMode) (videoStream, error) {
	return openVideo(dev, mode)
}

func (b *v4l2Backend) listControls(dev string) ([]videoControl, error) { return listControls(dev) }

func (b *v4l2Backend) setControl(dev string, id uint32, value int32) (int32, error) {
	return setControl(dev, id, value)
}

func (b *v4l2Backend) audioDevice() (string, error) {
	if b.audio != "" {
		return b.audio, nil
	}
	return findAudioCard(b.match)
}

func (b *v4l2Backend) runAudio(dev string, h *hub, stop <-chan struct{}) error {
	return runAudioUntil(dev, h, stop)
}
