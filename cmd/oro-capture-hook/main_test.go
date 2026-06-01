package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureHook_FailOpenOnError(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".oro"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-bad")

	var out bytes.Buffer
	run(strings.NewReader("{not json"), &out)

	if got := strings.TrimSpace(out.String()); got != "{}" {
		t.Fatalf("stdout = %q, want {}", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".oro", "capture-oro-bad.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("malformed payload wrote capture buffer, stat err = %v", err)
	}
}

func TestCaptureHook_PrivacyStrip(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".oro"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-private")

	input := `{
		"hook_event_name":"PostToolUse",
		"cwd":` + strconvQuote(cwd) + `,
		"tool_name":"Bash",
		"tool_input":{"command":"echo <private>/secret"},
		"tool_response":{"stdout":"ok <system-reminder>hidden</system-reminder>"}
	}`

	var out bytes.Buffer
	run(strings.NewReader(input), &out)

	if got := strings.TrimSpace(out.String()); got != "{}" {
		t.Fatalf("stdout = %q, want {}", got)
	}

	data, err := os.ReadFile(filepath.Join(cwd, ".oro", "capture-oro-private.jsonl"))
	if err != nil {
		t.Fatalf("read capture buffer: %v", err)
	}
	if strings.Contains(string(data), "/secret") || strings.Contains(string(data), "hidden") {
		t.Fatalf("private content reached buffer:\n%s", data)
	}
	if !strings.Contains(string(data), "[redacted]") {
		t.Fatalf("redaction marker missing from buffer:\n%s", data)
	}
}

func TestCaptureBuffer_BoundedAndMissingDirTolerant(t *testing.T) {
	t.Run("over capacity drops oldest records", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.Mkdir(filepath.Join(cwd, ".oro"), 0o750); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORO_WORKER_BEAD_ID", "oro-bounded")
		t.Setenv("ORO_CAPTURE_BUFFER_BYTES", "260")

		for _, marker := range []string{"oldest", "middle", "newest"} {
			input := `{"hook_event_name":"PostToolUse","cwd":` + strconvQuote(cwd) + `,"tool_name":"Bash","tool_input":{"command":` + strconvQuote(marker) + `},"tool_response":{"stdout":"` + strings.Repeat("x", 80) + `"}}`
			run(strings.NewReader(input), &bytes.Buffer{})
		}

		data, err := os.ReadFile(filepath.Join(cwd, ".oro", "capture-oro-bounded.jsonl"))
		if err != nil {
			t.Fatalf("read capture buffer: %v", err)
		}
		if strings.Contains(string(data), "oldest") {
			t.Fatalf("oldest record was not dropped:\n%s", data)
		}
		if !strings.Contains(string(data), "newest") {
			t.Fatalf("newest record missing after bounded write:\n%s", data)
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			var record captureRecord
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				t.Fatalf("buffer contains malformed JSONL record %q: %v", scanner.Text(), err)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing .oro directory skips without error", func(t *testing.T) {
		cwd := t.TempDir()
		t.Setenv("ORO_WORKER_BEAD_ID", "oro-missing")

		input := `{"hook_event_name":"PostToolUse","cwd":` + strconvQuote(cwd) + `,"tool_name":"Bash","tool_input":{"command":"true"},"tool_response":{"stdout":"ok"}}`
		var out bytes.Buffer
		run(strings.NewReader(input), &out)

		if got := strings.TrimSpace(out.String()); got != "{}" {
			t.Fatalf("stdout = %q, want {}", got)
		}
		if _, err := os.Stat(filepath.Join(cwd, ".oro")); !os.IsNotExist(err) {
			t.Fatalf("missing .oro dir should not be created, stat err = %v", err)
		}
	})
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
