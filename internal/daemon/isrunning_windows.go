//go:build windows

package daemon

import "golang.org/x/sys/windows"

// IsRunning reports whether the PID is alive.
func IsRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(handle)
	return true
}