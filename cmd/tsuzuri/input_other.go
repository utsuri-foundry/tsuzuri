//go:build !linux && !darwin

package main

import "os"

func openInputFile(path string) (*os.File, error) {
	return nil, usage("only Linux and macOS are supported")
}

func prepareInput(file *os.File) (*os.File, func() error, error) {
	return nil, nil, usage("only Linux and macOS are supported")
}
