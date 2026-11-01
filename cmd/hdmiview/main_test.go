package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRetryLoggingIsQuiet(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	defer func(d time.Duration) { retryInterval = d }(retryInterval)
	retryInterval = 10 * time.Millisecond

	h := newHub(1)
	ch := h.subscribe() // a viewer is watching
	defer h.unsubscribe(ch)

	attempts := 0
	foundAt := make(chan struct{})
	go runWhileWatched("video", h, func(stop <-chan struct{}, started func(...any)) error {
		attempts++
		switch {
		case attempts <= 5:
			return errors.New("device busy")
		case attempts == 6:
			return errors.New("device not found")
		}
		started("device", "/dev/video0")
		close(foundAt)
		<-stop
		return nil
	})
	select {
	case <-foundAt:
	case <-time.After(30 * time.Second):
		t.Fatal("source never started")
	}
	time.Sleep(50 * time.Millisecond)

	out := logs.String()
	count := func(s string) int { return strings.Count(out, s) }
	if n := count("device not available, retrying"); n != 2 {
		t.Errorf("want 2 retry warnings (one per distinct error), got %d:\n%s", n, out)
	}
	if count("retryEvery=10ms") != 2 || count("device busy") != 1 || count("device not found") != 1 {
		t.Errorf("warnings should name the error and the retry interval:\n%s", out)
	}
	if count("video device found") != 1 || !strings.Contains(out, "unavailableFor=") {
		t.Errorf("want one 'device found' with the outage duration:\n%s", out)
	}
	if count("capture started") != 0 {
		t.Errorf("recovery should be reported as 'found', not 'started':\n%s", out)
	}
}
