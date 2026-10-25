//go:build !linux && !darwin

package tsuzuri

import (
	"os"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd) error {
	return invalid("supported operating systems are Linux and macOS")
}
func openRecord(path string) (*os.File, error) {
	return nil, invalid("supported operating systems are Linux and macOS")
}
