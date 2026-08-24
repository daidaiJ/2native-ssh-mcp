//go:build !windows

package config

import (
	"fmt"
	"os"
)

func checkConfigFileMode(path string, info os.FileInfo) error {
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		return fmt.Errorf(
			"config file %s has insecure permissions %04o (group/other can read or write); run chmod 600 %s or pass --allow-insecure-config-perms",
			path, mode, path,
		)
	}
	return nil
}

func checkConfigDirMode(path string, info os.FileInfo) error {
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		return fmt.Errorf(
			"config directory %s has insecure permissions %04o (group/other can access); run chmod 700 %s or pass --allow-insecure-config-perms",
			path, mode, path,
		)
	}
	return nil
}
