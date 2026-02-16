package protocol_test

import (
	"testing"

	"oro/pkg/protocol"
)

func TestSanitizeFTS5Query_EmptyAndSpecialChars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"single word", "hello", `"hello"`},
		{"strips quotes", `he"llo wo"rld`, `"hello" OR "world"`},
		{"multiple words", "hello world", `"hello" OR "world"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocol.SanitizeFTS5Query(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFTS5Query(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
