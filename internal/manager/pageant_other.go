//go:build !windows

package manager

import (
	"fmt"
	"net"
)

// pageantConn is only available on Windows.
func pageantConn() (net.Conn, error) {
	return nil, fmt.Errorf("pageant is only supported on Windows")
}