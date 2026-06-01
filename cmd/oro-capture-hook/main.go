// Binary oro-capture-hook is a PostToolUse hook that appends privacy-stripped
// tool events to a bounded per-bead JSONL buffer.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBufferBytes = 200_000
	redactionMarker    = "[redacted]"
)

var allowJSON = []byte("{}")

type hookInput struct {
	HookType      string          `json:"hook_type,omitempty"`
	HookEventName string          `json:"hook_event_name,omitempty"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolResponse  json.RawMessage `json:"tool_response"`
	CWD           string          `json:"cwd"`
}

type captureRecord struct {
	Timestamp    string          `json:"ts"`
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
	CWD          string          `json:"cwd"`
}

func run(r io.Reader, w io.Writer) {
	input, err := io.ReadAll(r)
	if err == nil {
		_ = handleHook(input)
	}
	writeOut(w, allowJSON)
}

func handleHook(input []byte) error {
	var hook hookInput
	if err := json.Unmarshal(input, &hook); err != nil {
		return nil //nolint:nilerr // fail-open hook: malformed payload must not block tool use
	}
	if hook.ToolName == "" {
		return nil
	}
	beadID := os.Getenv("ORO_WORKER_BEAD_ID")
	if beadID == "" {
		return nil
	}

	cwd := hook.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil //nolint:nilerr // fail-open hook: cwd lookup failure skips capture
		}
	}
	bufferPath, ok := captureBufferPath(cwd, beadID)
	if !ok {
		return nil
	}

	record := captureRecord{
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		ToolName:     hook.ToolName,
		ToolInput:    sanitizeRawJSON(hook.ToolInput),
		ToolResponse: sanitizeRawJSON(hook.ToolResponse),
		CWD:          stripPrivateText(cwd),
	}
	line, err := json.Marshal(record)
	if err != nil {
		return nil //nolint:nilerr // fail-open hook: unexpected marshal failure skips capture
	}
	return appendBoundedLine(bufferPath, append(line, '\n'), bufferLimit())
}

func captureBufferPath(cwd, beadID string) (string, bool) {
	oroDir := filepath.Join(cwd, ".oro")
	info, err := os.Stat(oroDir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return filepath.Join(oroDir, "capture-"+safeKey(beadID)+".jsonl"), true
}

func safeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func bufferLimit() int {
	raw := os.Getenv("ORO_CAPTURE_BUFFER_BYTES")
	if raw == "" {
		return defaultBufferBytes
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultBufferBytes
	}
	return n
}

func appendBoundedLine(path string, line []byte, limit int) error {
	existing, err := os.ReadFile(path) //nolint:gosec // path is constrained by captureBufferPath to cwd/.oro/capture-<safe bead>.jsonl
	if err != nil && !os.IsNotExist(err) {
		return nil
	}

	lines := splitJSONLLines(existing)
	lines = append(lines, line)
	for totalLineBytes(lines) > limit && len(lines) > 1 {
		lines = lines[1:]
	}
	if err := os.WriteFile(path, bytes.Join(lines, nil), 0o600); err != nil { //nolint:gosec // path is constrained by captureBufferPath to cwd/.oro/capture-<safe bead>.jsonl
		return fmt.Errorf("write capture buffer: %w", err)
	}
	return nil
}

func splitJSONLLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	rawLines := bytes.SplitAfter(data, []byte("\n"))
	lines := make([][]byte, 0, len(rawLines))
	for _, line := range rawLines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func totalLineBytes(lines [][]byte) int {
	total := 0
	for _, line := range lines {
		total += len(line)
	}
	return total
}

func sanitizeRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`"[redacted]"`)
	}
	value = sanitizeValue(value)
	out, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`"[redacted]"`)
	}
	return out
}

func sanitizeValue(value any) any {
	switch v := value.(type) {
	case string:
		return stripPrivateText(v)
	case []any:
		for i := range v {
			v[i] = sanitizeValue(v[i])
		}
		return v
	case map[string]any:
		for k, elem := range v {
			v[k] = sanitizeValue(elem)
		}
		return v
	default:
		return v
	}
}

var privateTagREs = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<private\b[^>]*>.*?</private>`),
	regexp.MustCompile(`(?is)<system-reminder\b[^>]*>.*?</system-reminder>`),
	regexp.MustCompile(`(?is)<private>\S*`),
	regexp.MustCompile(`(?is)<system-reminder>\S*`),
}

func stripPrivateText(s string) string {
	for _, re := range privateTagREs {
		s = re.ReplaceAllString(s, redactionMarker)
	}
	return s
}

func main() {
	run(os.Stdin, os.Stdout)
}

func writeOut(w io.Writer, data []byte) {
	if _, err := w.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "oro-capture-hook: stdout write error: %v\n", err)
	}
}
