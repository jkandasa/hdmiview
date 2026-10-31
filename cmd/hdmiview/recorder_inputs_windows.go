package main

func newRecInputs(withAudio bool) (recInput, error) { return newStdinTCPInputs(withAudio) }
