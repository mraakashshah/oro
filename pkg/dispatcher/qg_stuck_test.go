package dispatcher //nolint:testpackage // white-box tests for hashQGOutput

import (
	"strings"
	"testing"
)

func TestHashQGOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "empty string",
			output: "",
		},
		{
			name:   "simple string",
			output: "hello world",
		},
		{
			name:   "QG output with newlines",
			output: "Task: Fix bug\nStatus: In Progress\nNext: Run tests",
		},
		{
			name:   "large output",
			output: strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 1000),
		},
		{
			name:   "unicode characters",
			output: "Task: 修复错误 🐛 — Status: ✅ Complete",
		},
		{
			name:   "JSON-like output",
			output: `{"task": "fix-bug", "status": "in_progress", "next": ["test", "commit"]}`,
		},
		{
			name:   "whitespace variations",
			output: "   spaces\ttabs\n\nnewlines   ",
		},
		{
			name:   "special characters",
			output: "!@#$%^&*()_+-=[]{}|;':\",./<>?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := hashQGOutput(tt.output)

			// Hash should be 64 hex characters (SHA-256 produces 32 bytes = 64 hex chars)
			if len(hash) != 64 {
				t.Errorf("HashQGOutput(%q) hash length = %d, want 64", tt.output, len(hash))
			}

			// Hash should only contain hex characters (0-9a-f)
			for _, ch := range hash {
				if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
					t.Errorf("HashQGOutput(%q) hash contains non-hex character: %c", tt.output, ch)
					break
				}
			}

			// Hash should be deterministic - same input produces same output
			hash2 := hashQGOutput(tt.output)
			if hash != hash2 {
				t.Errorf("HashQGOutput(%q) not deterministic: got %q and %q", tt.output, hash, hash2)
			}
		})
	}
}

func TestHashQGOutput_Uniqueness(t *testing.T) {
	// Different inputs should produce different hashes
	tests := []struct {
		name    string
		output1 string
		output2 string
	}{
		{
			name:    "different strings",
			output1: "hello",
			output2: "world",
		},
		{
			name:    "case sensitivity",
			output1: "Hello",
			output2: "hello",
		},
		{
			name:    "trailing whitespace",
			output1: "test",
			output2: "test ",
		},
		{
			name:    "newline difference",
			output1: "line1\nline2",
			output2: "line1 line2",
		},
		{
			name:    "empty vs space",
			output1: "",
			output2: " ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := hashQGOutput(tt.output1)
			hash2 := hashQGOutput(tt.output2)

			if hash1 == hash2 {
				t.Errorf("HashQGOutput produced same hash for different inputs:\n  input1: %q\n  input2: %q\n  hash: %q",
					tt.output1, tt.output2, hash1)
			}
		})
	}
}

func TestHashQGOutput_KnownVector(t *testing.T) {
	// Test against a known SHA-256 hash to ensure correctness
	// echo -n "test" | sha256sum produces: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
	output := "test"
	want := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	got := hashQGOutput(output)
	if got != want {
		t.Errorf("HashQGOutput(%q) = %q, want %q", output, got, want)
	}
}
