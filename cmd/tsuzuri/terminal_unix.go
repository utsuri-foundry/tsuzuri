//go:build linux || darwin

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func isTerminal(file *os.File) bool {
	var size [4]uint16
	// Query the terminal without changing its mode; a character device alone
	// is insufficient because /dev/null is also one.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	return errno == 0
}
