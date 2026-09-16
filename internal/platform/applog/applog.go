// Package applog is the in-memory host log ring plus optional stdout and file sinks.
package applog

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/KroniK907/hackbox/internal/platform/hub"
)

const ringSize = 500

// Logger keeps the last 500 lines in process memory. First paint never reads
// host.log. New lines publish a named log SSE event.
type Logger struct {
	mu       sync.Mutex
	lines    []string
	events   *hub.Hub
	stdout   bool
	fileOn   bool
	filePath string
}

// New creates an empty ring. filePath is host.log in the data dir.
func New(events *hub.Hub, filePath string) *Logger {
	return &Logger{events: events, filePath: filePath}
}

// SetSinks turns stdout and file append on or off. Defaults are off.
func (l *Logger) SetSinks(stdout, file bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stdout = stdout
	l.fileOn = file
}

// Write appends one short line to the ring and live sinks.
func (l *Logger) Write(line string) {
	line = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(line), "\r", ""), "\n", " ")
	if line == "" {
		return
	}
	l.mu.Lock()
	l.lines = append(l.lines, line)
	if len(l.lines) > ringSize {
		l.lines = append([]string(nil), l.lines[len(l.lines)-ringSize:]...)
	}
	stdout := l.stdout
	fileOn := l.fileOn
	path := l.filePath
	l.mu.Unlock()

	if l.events != nil {
		l.events.PublishData("log", line)
	}
	if stdout {
		fmt.Println(line)
	}
	if fileOn && path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, line)
			_ = f.Close()
		}
	}
}

// Lines returns a copy of the ring, oldest first.
func (l *Logger) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}
