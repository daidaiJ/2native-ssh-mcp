// Package daemon implements the HTTP daemon lifecycle: PID file, admin
// endpoints and the start/stop/status client used by the CLI subcommands.
package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PIDInfo is stored in the PID file.
type PIDInfo struct {
	PID  int `json:"pid"`
	Port int `json:"port"`
}

// PIDFileName returns the PID file path, preferring the executable directory.
func PIDFileName() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), ".ssh-mcp-server.pid")
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, ".ssh-mcp-server.pid")
	}
	return filepath.Join(os.TempDir(), "ssh-mcp-server.pid")
}

// ReadPID reads the PID file.
func ReadPID() (*PIDInfo, error) {
	data, err := os.ReadFile(PIDFileName())
	if err != nil {
		return nil, err
	}
	var info PIDInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// WritePID writes the PID file.
func WritePID(pid, port int) error {
	data, err := json.Marshal(PIDInfo{PID: pid, Port: port})
	if err != nil {
		return err
	}
	return os.WriteFile(PIDFileName(), data, 0o644)
}

// RemovePID deletes the PID file.
func RemovePID() error {
	return os.Remove(PIDFileName())
}

// AdminURL builds an admin API URL.
func AdminURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/__admin%s", port, path)
}

// RefCountResponse is the admin API response shape.
type RefCountResponse struct {
	Name     string `json:"name"`
	RefCount int    `json:"ref_count"`
	Message  string `json:"message,omitempty"`
}

// ServerName identifies this daemon in admin responses so clients can tell
// it apart from other services on the same host.
const ServerName = "ssh-mcp-server"

// GetHealth probes whether the daemon is running.
func GetHealth(port int) (*RefCountResponse, error) {
	return adminGet(port, "/health")
}

// GetStatus returns the daemon status.
func GetStatus(port int) (*RefCountResponse, error) {
	return adminGet(port, "/status")
}

// PostRefCount changes the daemon refcount by delta.
func PostRefCount(port, delta int) (*RefCountResponse, error) {
	url := AdminURL(port, "/refcount")
	body := fmt.Sprintf(`{"delta":%d}`, delta)
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result RefCountResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Name != ServerName {
		return nil, fmt.Errorf("port %d is not an ssh-mcp-server daemon", port)
	}
	return &result, nil
}

// PostShutdown requests the daemon to shut down.
func PostShutdown(port int) error {
	_, err := http.Post(AdminURL(port, "/shutdown"), "application/json", nil)
	return err
}

func adminGet(port int, path string) (*RefCountResponse, error) {
	resp, err := http.Get(AdminURL(port, path))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result RefCountResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Name != ServerName {
		return nil, fmt.Errorf("port %d is not an ssh-mcp-server daemon", port)
	}
	return &result, nil
}

// WaitForExit polls the health endpoint until the daemon stops.
func WaitForExit(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := GetHealth(port); err != nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// KillProcess force-kills the process.
func KillProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}