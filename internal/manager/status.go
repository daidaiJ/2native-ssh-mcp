package manager

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"ssh-mcp-server-go/internal/logger"
)

// ServerStatus is the system status collected from a remote server.
type ServerStatus struct {
	Reachable     bool       `json:"reachable"`
	Hostname      string     `json:"hostname,omitempty"`
	IPAddresses   []string   `json:"ipAddresses,omitempty"`
	OSName        string     `json:"osName,omitempty"`
	OSVersion     string     `json:"osVersion,omitempty"`
	KernelVersion string     `json:"kernelVersion,omitempty"`
	Uptime        string     `json:"uptime,omitempty"`
	DiskSpace     *DiskSpace `json:"diskSpace,omitempty"`
	Memory        *Memory    `json:"memory,omitempty"`
	CPU           *CPU       `json:"cpu,omitempty"`
	Processes     *Processes `json:"processes,omitempty"`
	LastUpdated   string     `json:"lastUpdated,omitempty"`
}

// DiskSpace is the free/total space of the root filesystem.
type DiskSpace struct {
	Free  string `json:"free"`
	Total string `json:"total"`
}

// Memory is the free/total memory.
type Memory struct {
	Free  string `json:"free"`
	Total string `json:"total"`
}

// CPU describes the remote CPU.
type CPU struct {
	Name  string `json:"name,omitempty"`
	Usage string `json:"usage,omitempty"`
}

// Processes counts running processes and threads.
type Processes struct {
	Running int `json:"running"`
	Threads int `json:"threads"`
}

// statusProbes are collected in a single remote command, each result
// introduced by a marker line.
var statusProbes = map[string]string{
	"hostname":      "hostname",
	"ipAddresses":   "ip -o addr show | awk '{print $4}' | grep -v '^127\\.' | cut -d'/' -f1",
	"osName":        "uname -s",
	"osVersion":     "cat /etc/os-release 2>/dev/null | grep '^PRETTY_NAME=' | cut -d'=' -f2 | tr -d '\"' || uname -o",
	"kernelVersion": "uname -r",
	"uptime":        "uptime -p 2>/dev/null || uptime | awk -F'up ' '{print $2}' | awk -F',' '{print $1}'",
	"diskSpace":     "df -h / | tail -1 | awk '{print \"free:\" $4 \" total:\" $2}'",
	"memory":        "free -h | grep '^Mem:' | awk '{print \"free:\" $7 \" total:\" $2}'",
	"cpuName":       "sh -c '(lscpu 2>/dev/null | grep \"^Model name:\" | cut -d\":\" -f2 | xargs || cat /proc/cpuinfo 2>/dev/null | grep \"model name\" | head -1 | cut -d\":\" -f2 | xargs || echo \"$(nproc 2>/dev/null || echo '?')\"-core $(uname -m 2>/dev/null || echo 'unknown') processor\") || true'",
	"cpuUsage":      "top -bn1 | grep 'Cpu(s)' | sed 's/.*, *\\([0-9.]*\\)%* id.*/\\1/' | awk '{print 100 - $1}'",
	"processes":     "ps aux | wc -l",
	"threads":       "ps -eLf | wc -l",
}

// scheduleStatus collects system status shortly after connecting.
func (m *Manager) scheduleStatus(key string) {
	go func() {
		time.Sleep(time.Second)
		m.collectStatus(key)
	}()
}

// collectStatus runs the status probes and caches the result.
func (m *Manager) collectStatus(key string) {
	status := &ServerStatus{Reachable: true, LastUpdated: time.Now().Format(time.RFC3339)}
	defer func() {
		m.mu.Lock()
		m.statuses[key] = status
		m.mu.Unlock()
	}()

	marker := fmt.Sprintf("__MCP_FIELD_%x_", time.Now().UnixNano())
	var probes []string
	for field, command := range statusProbes {
		probes = append(probes, fmt.Sprintf("printf '\\n%s%s\\n'; { %s; } 2>/dev/null", marker, field, command))
	}
	script := strings.Join(probes, "; ") + "; true"

	output, _, err := m.runCommandInternal(key, script, RunOptions{Prevalidated: true})
	if err != nil {
		logger.Error("Failed to collect system status for [%s]: %v", key, err)
		return
	}

	values := parseStatusOutput(output, marker)
	readField := func(field string) string { return values[field] }

	if v := readField("hostname"); v != "" {
		status.Hostname = v
	}
	if v := readField("ipAddresses"); v != "" {
		for _, ip := range strings.Split(v, "\n") {
			ip = strings.TrimSpace(ip)
			if ip != "" && !strings.Contains(ip, "127.0.0.1") {
				status.IPAddresses = append(status.IPAddresses, ip)
			}
		}
	}
	if v := readField("osName"); v != "" {
		status.OSName = v
	}
	if v := readField("osVersion"); v != "" {
		status.OSVersion = v
	}
	if v := readField("kernelVersion"); v != "" {
		status.KernelVersion = v
	}
	if v := readField("uptime"); v != "" {
		status.Uptime = v
	}
	if v := readField("diskSpace"); v != "" {
		if m := regexp.MustCompile(`free:(\S+)\s+total:(\S+)`).FindStringSubmatch(v); m != nil {
			status.DiskSpace = &DiskSpace{Free: m[1], Total: m[2]}
		}
	}
	if v := readField("memory"); v != "" {
		if m := regexp.MustCompile(`free:(\S+)\s+total:(\S+)`).FindStringSubmatch(v); m != nil {
			status.Memory = &Memory{Free: m[1], Total: m[2]}
		}
	}
	if v := strings.TrimSpace(readField("cpuName")); v != "" {
		status.CPU = &CPU{Name: v}
	}
	if status.CPU != nil {
		if v := readField("cpuUsage"); v != "" && v != "N/A" {
			var usage float64
			if _, err := fmt.Sscanf(v, "%f", &usage); err == nil {
				status.CPU.Usage = fmt.Sprintf("%.1f%%", usage)
			}
		}
	}
	if v := readField("processes"); v != "" {
		var running int
		if _, err := fmt.Sscanf(v, "%d", &running); err == nil {
			status.Processes = &Processes{Running: running}
		}
	}
	if status.Processes != nil {
		if v := readField("threads"); v != "" {
			var threads int
			if _, err := fmt.Sscanf(v, "%d", &threads); err == nil {
				status.Processes.Threads = threads
			}
		}
	}
}

// parseStatusOutput splits the probe output on marker lines. Each segment
// after the preamble is "field\nvalue".
func parseStatusOutput(output, marker string) map[string]string {
	values := map[string]string{}
	normalized := strings.ReplaceAll(strings.ReplaceAll(output, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(normalized, marker)
	for _, part := range parts[1:] {
		fieldEnd := strings.Index(part, "\n")
		if fieldEnd == -1 {
			continue
		}
		field := part[:fieldEnd]
		value := strings.TrimSpace(part[fieldEnd+1:])
		values[field] = value
	}
	return values
}

// runCommandInternal runs a command without whitelist validation.
func (m *Manager) runCommandInternal(key, command string, opts RunOptions) (string, int, error) {
	cfg, err := m.getConfig(key)
	if err != nil {
		return "", -1, err
	}
	client, err := m.EnsureConnected(key)
	if err != nil {
		return "", -1, err
	}
	timeout := time.Duration(cfg.CommandTimeoutMs) * time.Millisecond
	if cfg.TransportMode == "shell" {
		return m.runShellCommand(key, command, "", timeout)
	}
	return m.runExecCommand(client, cfg, command, "", timeout, key)
}