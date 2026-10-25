//go:build !linux && !darwin

package main

import "os"

func isTerminal(*os.File) bool { return false }
