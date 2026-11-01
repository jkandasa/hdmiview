package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

// findAudioCard returns the ALSA id of the first card whose /proc/asound/cards
// entry contains match.
func findAudioCard(match string) (string, error) {
	f, err := os.Open("/proc/asound/cards")
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// " 2 [MS2109         ]: USB-Audio - MS2109"
		if !strings.Contains(line, match) {
			continue
		}
		if i, j := strings.Index(line, "["), strings.Index(line, "]"); i >= 0 && j > i {
			return "plughw:CARD=" + strings.TrimSpace(line[i+1:j]) + ",DEV=0", nil // plughw converts if the firmware offers another format
		}
	}
	return "", fmt.Errorf("no ALSA card matching %q", match)
}

// runAudioUntil streams raw PCM from arecord into hub until stop closes
// (returns nil) or arecord fails.
func runAudioUntil(device string, hub *hub, stop <-chan struct{}) error {
	cmd := exec.Command("arecord", "-q", "-D", device, "-f", "S16_LE",
		"-c", fmt.Sprint(audioChannels), "-r", fmt.Sprint(audioRate),
		"-t", "raw", "--buffer-time=100000", "--period-time=10000")
	stderr := &tailBuffer{max: 1024}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	slog.Debug("arecord started", "device", device)

	var killed atomic.Bool
	exited := make(chan struct{})
	go func() {
		select {
		case <-stop:
			killed.Store(true)
			cmd.Process.Kill()
		case <-exited:
		}
	}()
	for {
		buf := make([]byte, audioChunk)
		if _, err := io.ReadFull(out, buf); err != nil {
			close(exited)
			cmd.Process.Kill()
			cmd.Wait()
			if killed.Load() {
				return nil
			}
			if msg := lastLine(strings.TrimSpace(stderr.String())); msg != "" {
				return fmt.Errorf("%s", msg)
			}
			return fmt.Errorf("arecord: %w", err)
		}
		hub.publish(buf)
	}
}
