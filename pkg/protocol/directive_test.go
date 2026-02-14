package protocol_test

import (
	"encoding/json"
	"testing"

	"oro/pkg/protocol"
)

func TestDirectiveConstants(t *testing.T) {
	tests := []struct {
		d    protocol.Directive
		want string
	}{
		{protocol.DirectiveStart, "start"},
		{protocol.DirectiveStop, "stop"},
		{protocol.DirectivePause, "pause"},
		{protocol.DirectiveFocus, "focus"},
		{protocol.DirectiveShutdown, "shutdown"},
	}
	for _, tc := range tests {
		if string(tc.d) != tc.want {
			t.Errorf("expected %q, got %q", tc.want, string(tc.d))
		}
	}
}

func TestDirectiveValid(t *testing.T) {
	valid := []protocol.Directive{protocol.DirectiveStart, protocol.DirectiveStop, protocol.DirectivePause, protocol.DirectiveFocus, protocol.DirectiveShutdown}
	for _, d := range valid {
		if !d.Valid() {
			t.Errorf("expected %q to be valid", d)
		}
	}

	invalid := []protocol.Directive{"restart", "unknown", "", "STOP"}
	for _, d := range invalid {
		if d.Valid() {
			t.Errorf("expected %q to be invalid", d)
		}
	}
}

func TestCommandJSON(t *testing.T) {
	cmd := protocol.Command{
		ID:        42,
		Ts:        "2025-01-15T10:30:00Z",
		Directive: string(protocol.DirectiveFocus),
		Target:    "epic-123",
		Processed: false,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var got protocol.Command
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if got != cmd {
		t.Errorf("round-trip mismatch:\n  want: %+v\n  got:  %+v", cmd, got)
	}

	// Verify JSON field names
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map error: %v", err)
	}

	expectedKeys := []string{"id", "ts", "directive", "target", "processed"}
	for _, k := range expectedKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("expected JSON key %q not found in marshaled output", k)
		}
	}
}
