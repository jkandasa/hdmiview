//go:build !windows

package main

import (
	"io"
	"os"
	"os/exec"
)

func newRecInputs(withAudio bool) (recInput, error) { return newPipeInputs(withAudio) }

// pipeInputs feeds ffmpeg's two inputs through extra pipes (fd 3 and 4).
type pipeInputs struct {
	vr, vw, ar, aw *os.File
}

func newPipeInputs(withAudio bool) (recInput, error) {
	in := &pipeInputs{}
	var err error
	if in.vr, in.vw, err = os.Pipe(); err != nil {
		return nil, err
	}
	if withAudio {
		if in.ar, in.aw, err = os.Pipe(); err != nil {
			in.abort()
			return nil, err
		}
	}
	return in, nil
}

func (in *pipeInputs) videoURL() string { return "pipe:3" }
func (in *pipeInputs) audioURL() string { return "pipe:4" }

func (in *pipeInputs) attach(cmd *exec.Cmd) error {
	cmd.ExtraFiles = []*os.File{in.vr}
	if in.ar != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, in.ar)
	}
	return nil
}

// started closes our copies of the read ends; ffmpeg holds its own.
func (in *pipeInputs) started() {
	in.vr.Close()
	if in.ar != nil {
		in.ar.Close()
	}
}

func (in *pipeInputs) video() io.WriteCloser          { return in.vw }
func (in *pipeInputs) audio() (io.WriteCloser, error) { return in.aw, nil }

func (in *pipeInputs) abort() {
	for _, f := range []*os.File{in.vr, in.vw, in.ar, in.aw} {
		if f != nil {
			f.Close()
		}
	}
}
