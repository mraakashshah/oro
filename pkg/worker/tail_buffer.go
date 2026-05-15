package worker

import (
	"strings"
	"sync"
)

// LineTailBuffer is an io.Writer that keeps only the last N complete lines.
type LineTailBuffer struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial string
}

// NewLineTailBuffer returns a line-oriented tail buffer. A non-positive limit
// keeps the default 100 lines.
func NewLineTailBuffer(limit int) *LineTailBuffer {
	if limit <= 0 {
		limit = 100
	}
	return &LineTailBuffer{max: limit}
}

// Write appends bytes to the rolling line buffer.
func (b *LineTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	parts := strings.SplitAfter(b.partial+string(p), "\n")
	b.partial = ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, "\n") {
			b.appendLine(strings.TrimRight(part, "\r\n"))
			continue
		}
		b.partial = part
	}
	return len(p), nil
}

func (b *LineTailBuffer) appendLine(line string) {
	b.lines = append(b.lines, line)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

// String returns the retained lines joined by newlines.
func (b *LineTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	lines := append([]string(nil), b.lines...)
	if b.partial != "" {
		lines = append(lines, b.partial)
	}
	if len(lines) > b.max {
		lines = lines[len(lines)-b.max:]
	}
	return strings.Join(lines, "\n")
}
