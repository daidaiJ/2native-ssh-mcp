// Package logger writes all log output to stderr so it never interferes
// with the MCP stdio protocol on stdout.
package logger

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var mu sync.Mutex

// Log writes a timestamped message to stderr.
func Log(level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	ts := time.Now().Format(time.RFC3339)
	fmt.Fprintf(os.Stderr, "[%s] [%s] %s\n", ts, level, fmt.Sprintf(format, args...))
}

// Info logs an informational message.
func Info(format string, args ...any) { Log("INFO", format, args...) }

// Error logs an error message.
func Error(format string, args ...any) { Log("ERROR", format, args...) }

// Debug logs a debug message.
func Debug(format string, args ...any) { Log("DEBUG", format, args...) }