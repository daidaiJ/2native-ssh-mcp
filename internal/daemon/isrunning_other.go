//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// IsRunning reports whether the PID is alive.
func IsRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}