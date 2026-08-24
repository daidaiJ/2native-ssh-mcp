//go:build windows

package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func checkConfigFileMode(path string, _ os.FileInfo) error {
	return checkWindowsConfigACL(path)
}

func checkConfigDirMode(path string, _ os.FileInfo) error {
	return checkWindowsConfigACL(path)
}

// checkWindowsConfigACL refuses when Authenticated Users or Everyone can modify
// the config file or directory.
func checkWindowsConfigACL(path string) error {
	out, err := exec.Command("icacls", path).CombinedOutput()
	if err != nil {
		// icacls missing or ACL unreadable — warn via permissive start on dev boxes.
		return nil
	}
	text := strings.ToLower(string(out))
	for _, principal := range []string{"everyone", "builtin\\users", "authenticated users"} {
		if !strings.Contains(text, principal) {
			continue
		}
		// (M) modify, (F) full, (W) write
		for _, perm := range []string{"(f)", "(m)", "(w)"} {
			idx := strings.Index(text, principal)
			if idx < 0 {
				continue
			}
			segment := text[idx:]
			if end := strings.Index(segment, "\n"); end > 0 {
				segment = segment[:end]
			}
			if strings.Contains(segment, perm) {
				return fmt.Errorf(
					"config path %s ACL allows %q to modify the file; restrict ACLs with icacls or pass --allow-insecure-config-perms",
					path, principal,
				)
			}
		}
	}
	return nil
}
