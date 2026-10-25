//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func openInputFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

// prepareInput makes inherited stdin pollable without transferring ownership of
// its descriptor. Duplicates share status flags, so cleanup restores the exact
// original blocking mode. The CLI has sole ownership of stdin during input.
func prepareInput(file *os.File) (*os.File, func() error, error) {
	if err := file.SetReadDeadline(time.Time{}); err == nil {
		return file, func() error { return nil }, nil
	}
	fd := file.Fd()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		return nil, nil, errno
	}
	syscall.ForkLock.RLock()
	dup, err := syscall.Dup(int(fd))
	if err == nil {
		syscall.CloseOnExec(dup)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	if err = syscall.SetNonblock(dup, true); err != nil {
		return nil, nil, errors.Join(err, syscall.Close(dup))
	}
	input := os.NewFile(uintptr(dup), file.Name())
	cleanup := func() error {
		restore := syscall.SetNonblock(int(fd), flags&syscall.O_NONBLOCK != 0)
		closeErr := input.Close()
		if errors.Is(closeErr, os.ErrClosed) {
			closeErr = nil
		}
		return errors.Join(restore, closeErr)
	}
	if err = input.SetReadDeadline(time.Time{}); err != nil {
		return nil, nil, errors.Join(usage("input does not support cancellation"), err, cleanup())
	}
	return input, cleanup, nil
}
