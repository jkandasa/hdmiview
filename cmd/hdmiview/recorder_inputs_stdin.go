package main

import (
	"io"
	"net"
	"os/exec"
	"time"
)

// stdinTCPInputs feeds ffmpeg where extra pipes cannot be passed to a child
// process (Windows): video goes through stdin, audio through a localhost
// TCP connection that ffmpeg opens to us. Available everywhere for tests.
type stdinTCPInputs struct {
	vw io.WriteCloser
	ln net.Listener
}

func newStdinTCPInputs(withAudio bool) (recInput, error) {
	in := &stdinTCPInputs{}
	if withAudio {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		in.ln = ln
	}
	return in, nil
}

func (in *stdinTCPInputs) videoURL() string { return "pipe:0" }
func (in *stdinTCPInputs) audioURL() string { return "tcp://" + in.ln.Addr().String() }

func (in *stdinTCPInputs) attach(cmd *exec.Cmd) error {
	w, err := cmd.StdinPipe()
	in.vw = w
	return err
}

func (in *stdinTCPInputs) started()              {}
func (in *stdinTCPInputs) video() io.WriteCloser { return in.vw }

// audio waits for ffmpeg to connect (it opens its inputs in order, after
// it has started reading the video).
func (in *stdinTCPInputs) audio() (io.WriteCloser, error) {
	in.ln.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
	c, err := in.ln.Accept()
	in.ln.Close()
	return c, err
}

func (in *stdinTCPInputs) abort() {
	if in.vw != nil {
		in.vw.Close()
	}
	if in.ln != nil {
		in.ln.Close()
	}
}
