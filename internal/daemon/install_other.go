//go:build !windows

package daemon

import "fmt"

// Install is only supported on Windows.
func Install() error {
	fmt.Println("Auto-start installation is not supported on this platform.")
	fmt.Println("Please configure your system's service manager (e.g., systemd) manually.")
	return nil
}

// Uninstall is only supported on Windows.
func Uninstall() error {
	fmt.Println("Auto-start uninstallation is not supported on this platform.")
	return nil
}