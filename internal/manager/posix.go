package manager

import (
	"path"
	"strings"
)

// posixIsAbs reports whether p is an absolute POSIX path. Remote SSH paths
// are always POSIX, so filepath.IsAbs must not be used (on Windows it
// rejects "/tmp/foo").
func posixIsAbs(p string) bool {
	return path.IsAbs(p)
}

// posixClean cleans a POSIX remote path without converting separators.
func posixClean(p string) string {
	if p == "" {
		return p
	}
	cleaned := path.Clean(p)
	if strings.HasSuffix(p, "/") && cleaned != "/" {
		return cleaned + "/"
	}
	return cleaned
}

// posixWithinRoot reports whether candidate is the POSIX root or a path
// under it. Both sides are cleaned first.
func posixWithinRoot(candidate, root string) bool {
	c := path.Clean(candidate)
	r := path.Clean(root)
	if r == "/" {
		return strings.HasPrefix(c, "/")
	}
	return c == r || strings.HasPrefix(c, r+"/")
}

// posixDir returns the parent directory of a POSIX path.
func posixDir(p string) string {
	d := path.Dir(p)
	if d == "." {
		return "/"
	}
	return d
}
