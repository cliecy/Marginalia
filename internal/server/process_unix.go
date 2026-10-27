//go:build unix

package server

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this ID exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
