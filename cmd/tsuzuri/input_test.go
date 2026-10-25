//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestInputRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if e := syscall.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e := readInput(ctx, path); e == nil {
		t.Fatal("accepted FIFO")
	}
}

type observedInputContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedInputContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestInputCancelsStdin(t *testing.T) {
	reader, writer, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	defer writer.Close()
	saved := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = saved }()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedInputContext{Context: parent, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := readInput(ctx, "-"); done <- err }()
	select {
	case <-ctx.observed:
	case <-time.After(time.Second):
		t.Fatal("input never registered cancellation")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("input read did not complete after cancellation")
	}
	if _, err := reader.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("cancellation cleanup not complete: %v", err)
	}
}

func TestInputFilesAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := readInput(context.Background(), path)
	if err != nil || string(body) != "{}" {
		t.Fatalf("regular input: %q %v", body, err)
	}
	link := path + "-link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readInput(context.Background(), link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.WriteFile(path, make([]byte, (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInput(context.Background(), path); err == nil {
		t.Fatal("accepted oversized input")
	}
}

func TestInputUnsupportedDevice(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	old := os.Stdin
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = old })
	if _, err := readInput(context.Background(), "-"); err == nil {
		t.Fatal("accepted non-pollable device")
	}
}

func TestCLIOutputFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = file
	t.Cleanup(func() { os.Stdout = old })
	for _, args := range [][]string{{"--version"}, {"--help"}, {"compatibility"}, {"compatibility", "--json"}} {
		if code := run(context.Background(), args); code == 0 {
			t.Fatalf("reported success after failed output: %v", args)
		}
	}
}

func TestInputInheritedPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	// Fd returns the descriptor in blocking mode; NewFile therefore models the
	// raw descriptor inherited from Rust, a shell, or another non-Go process.
	fd, err := syscall.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	inherited := os.NewFile(uintptr(fd), "inherited-stdin")
	defer inherited.Close()
	old := os.Stdin
	os.Stdin = inherited
	defer func() { os.Stdin = old }()
	if _, err := writer.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := readInput(context.Background(), "-")
	if err != nil || string(body) != "{}" {
		t.Fatalf("inherited pipe: %q %v", body, err)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_NONBLOCK != 0 {
		t.Fatalf("blocking mode not restored: %x %v", flags, errno)
	}
}

func TestInputCancelsInheritedPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	fd, err := syscall.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	inherited := os.NewFile(uintptr(fd), "inherited-stdin")
	defer inherited.Close()
	old := os.Stdin
	os.Stdin = inherited
	defer func() { os.Stdin = old }()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedInputContext{Context: parent, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := readInput(ctx, "-"); done <- err }()
	select {
	case <-ctx.observed:
	case <-time.After(time.Second):
		t.Fatal("input did not register cancellation")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("inherited input did not complete")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_NONBLOCK != 0 {
		t.Fatalf("blocking mode not restored after cancellation: %x %v", flags, errno)
	}
}
