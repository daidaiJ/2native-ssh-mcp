//go:build windows

package manager

import (
	"io"
	"net"
	"time"

	"golang.org/x/sys/windows"
)

// pageantConn connects to the Windows Pageant agent via its named pipe.
func pageantConn() (net.Conn, error) {
	pathp, err := windows.UTF16PtrFromString(`\\.\pipe\pageant`)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pathp,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return &pageantPipe{handle: handle}, nil
}

type pageantPipe struct {
	handle windows.Handle
}

func (p *pageantPipe) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(p.handle, b, &n, nil)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

func (p *pageantPipe) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(p.handle, b, &n, nil)
	return int(n), err
}

func (p *pageantPipe) Close() error {
	return windows.CloseHandle(p.handle)
}

func (p *pageantPipe) LocalAddr() net.Addr                { return nil }
func (p *pageantPipe) RemoteAddr() net.Addr               { return nil }
func (p *pageantPipe) SetDeadline(t time.Time) error      { return nil }
func (p *pageantPipe) SetReadDeadline(t time.Time) error  { return nil }
func (p *pageantPipe) SetWriteDeadline(t time.Time) error { return nil }