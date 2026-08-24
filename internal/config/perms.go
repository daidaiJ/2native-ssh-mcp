package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckConfigFilePermissions verifies that the config file is not world/group
// readable or writable. On Unix this enforces mode 0600/0400 and directory
// 0700. On Windows it checks ACL write access for non-owner principals.
func CheckConfigFilePermissions(path string) error {
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("config file path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("config file not found or unreadable: %s", resolved)
	}
	if info.IsDir() {
		return fmt.Errorf("config path is a directory: %s", resolved)
	}
	if err := checkConfigFileMode(resolved, info); err != nil {
		return err
	}
	dir := filepath.Dir(resolved)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("config directory not accessible: %s", dir)
	}
	return checkConfigDirMode(dir, dirInfo)
}
