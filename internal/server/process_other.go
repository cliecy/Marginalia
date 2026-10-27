//go:build !unix

package server

import "os"

// processAlive reports whether a process with this ID exists. (On Windows
// FindProcess fails for processes that do not exist; elsewhere it cannot
// tell, and the process is assumed to run.)
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
