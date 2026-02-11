package protocol_test

import (
	"encoding/json"
	"testing"

	"oro/pkg/protocol"
)

func TestMessageTypes(t *testing.T) {
	t.Parallel()

	// All expected message type constants must be defined.
	types := []protocol.MessageType{
		protocol.MsgAssign,
		protocol.MsgShutdown,
		protocol.MsgHeartbeat,
		protocol.MsgStatus,
		protocol.MsgHandoff,
		protocol.MsgDone,
		protocol.MsgReadyForReview,
		protocol.MsgReconnect,
		protocol.MsgReviewResult,
	}

	expected := []string{
		"ASSIGN",
		"SHUTDOWN",
		"HEARTBEAT",
		"STATUS",
		"HANDOFF",
		"DONE",
		"READY_FOR_REVIEW",
		"RECONNECT",
		"REVIEW_RESULT",
	}

	for i, mt := range types {
		if string(mt) != expected[i] {
			t.Errorf("expected %q, got %q", expected[i], mt)
		}
	}
}

func TestHandoffPayloadFields(t *testing.T) {
	t.Parallel()

	// Verify HandoffPayload includes the new typed context fields and they
	// marshal/unmarshal correctly through JSON round-trip.
	original := protocol.HandoffPayload{
		BeadID:         "bead-ctx-1",
		WorkerID:       "worker-ctx",
		Learnings:      []string{"ruff must run before pyright", "SQLite WAL requires single-writer"},
		Decisions:      []string{"use table-driven tests"},
		FilesModified:  []string{"pkg/protocol/message.go", "pkg/worker/worker.go"},
		ContextSummary: "Extended HandoffPayload with typed context fields for cross-session memory",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal HandoffPayload: %v", err)
	}

	var decoded protocol.HandoffPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal HandoffPayload: %v", err)
	}

	// Verify all fields survived round-trip
	if decoded.BeadID != original.BeadID {
		t.Errorf("BeadID: got %q, want %q", decoded.BeadID, original.BeadID)
	}
	if decoded.WorkerID != original.WorkerID {
		t.Errorf("WorkerID: got %q, want %q", decoded.WorkerID, original.WorkerID)
	}
	if len(decoded.Learnings) != len(original.Learnings) {
		t.Fatalf("Learnings len: got %d, want %d", len(decoded.Learnings), len(original.Learnings))
	}
	for i, l := range decoded.Learnings {
		if l != original.Learnings[i] {
			t.Errorf("Learnings[%d]: got %q, want %q", i, l, original.Learnings[i])
		}
	}
	if len(decoded.Decisions) != len(original.Decisions) {
		t.Fatalf("Decisions len: got %d, want %d", len(decoded.Decisions), len(original.Decisions))
	}
	for i, d := range decoded.Decisions {
		if d != original.Decisions[i] {
			t.Errorf("Decisions[%d]: got %q, want %q", i, d, original.Decisions[i])
		}
	}
	if len(decoded.FilesModified) != len(original.FilesModified) {
		t.Fatalf("FilesModified len: got %d, want %d", len(decoded.FilesModified), len(original.FilesModified))
	}
	for i, f := range decoded.FilesModified {
		if f != original.FilesModified[i] {
			t.Errorf("FilesModified[%d]: got %q, want %q", i, f, original.FilesModified[i])
		}
	}
	if decoded.ContextSummary != original.ContextSummary {
		t.Errorf("ContextSummary: got %q, want %q", decoded.ContextSummary, original.ContextSummary)
	}

	// Verify omitempty: empty slices and empty string should not appear in JSON
	emptyPayload := protocol.HandoffPayload{
		BeadID:   "bead-empty",
		WorkerID: "worker-empty",
	}
	emptyData, err := json.Marshal(emptyPayload)
	if err != nil {
		t.Fatalf("marshal empty payload: %v", err)
	}
	emptyJSON := string(emptyData)
	for _, field := range []string{"learnings", "decisions", "files_modified", "context_summary"} {
		if contains(emptyJSON, field) {
			t.Errorf("expected omitted field %q in JSON of empty payload, got: %s", field, emptyJSON)
		}
	}
}

// TestAssignPayloadMemoryContext verifies the new MemoryContext field on AssignPayload.
func TestAssignPayloadMemoryContext(t *testing.T) {
	t.Parallel()

	original := protocol.AssignPayload{
		BeadID:        "bead-mem-1",
		Worktree:      "/tmp/wt-mem",
		MemoryContext: "## Relevant Memories\n- [gotcha] ruff must run before pyright",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded protocol.AssignPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.MemoryContext != original.MemoryContext {
		t.Errorf("MemoryContext: got %q, want %q", decoded.MemoryContext, original.MemoryContext)
	}

	// Verify omitempty: empty MemoryContext should not appear
	emptyAssign := protocol.AssignPayload{BeadID: "b1", Worktree: "/tmp/wt"}
	emptyData, err := json.Marshal(emptyAssign)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if contains(string(emptyData), "memory_context") {
		t.Errorf("expected omitted memory_context in empty payload, got: %s", string(emptyData))
	}
}

// contains checks if substr exists in s (simple helper to avoid importing strings).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMessageJSON(t *testing.T) { //nolint:funlen // table-driven test with 8 message types
	t.Parallel()

	tests := []struct {
		name string
		msg  protocol.Message
	}{
		{
			name: "ASSIGN",
			msg: protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:   "bead-123",
					Worktree: "/tmp/worktree",
				},
			},
		},
		{
			name: "SHUTDOWN",
			msg: protocol.Message{
				Type: protocol.MsgShutdown,
			},
		},
		{
			name: "HEARTBEAT",
			msg: protocol.Message{
				Type: protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{
					BeadID:     "bead-456",
					WorkerID:   "worker-1",
					ContextPct: 42,
				},
			},
		},
		{
			name: "STATUS",
			msg: protocol.Message{
				Type: protocol.MsgStatus,
				Status: &protocol.StatusPayload{
					BeadID:   "bead-789",
					WorkerID: "worker-2",
					State:    "running",
					Result:   "ok",
				},
			},
		},
		{
			name: "HANDOFF",
			msg: protocol.Message{
				Type: protocol.MsgHandoff,
				Handoff: &protocol.HandoffPayload{
					BeadID:         "bead-abc",
					WorkerID:       "worker-3",
					Learnings:      []string{"learned something"},
					Decisions:      []string{"decided something"},
					FilesModified:  []string{"file.go"},
					ContextSummary: "summary of work",
				},
			},
		},
		{
			name: "DONE",
			msg: protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:   "bead-def",
					WorkerID: "worker-4",
				},
			},
		},
		{
			name: "DONE_with_quality_gate",
			msg: protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            "bead-qg",
					WorkerID:          "worker-qg",
					QualityGatePassed: true,
				},
			},
		},
		{
			name: "READY_FOR_REVIEW",
			msg: protocol.Message{
				Type: protocol.MsgReadyForReview,
				ReadyForReview: &protocol.ReadyForReviewPayload{
					BeadID:   "bead-ghi",
					WorkerID: "worker-5",
				},
			},
		},
		{
			name: "RECONNECT",
			msg: protocol.Message{
				Type: protocol.MsgReconnect,
				Reconnect: &protocol.ReconnectPayload{
					WorkerID:   "worker-6",
					BeadID:     "bead-jkl",
					State:      "paused",
					ContextPct: 75,
					BufferedEvents: []protocol.Message{
						{
							Type: protocol.MsgHeartbeat,
							Heartbeat: &protocol.HeartbeatPayload{
								BeadID:     "bead-jkl",
								WorkerID:   "worker-6",
								ContextPct: 70,
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}

			var got protocol.Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}

			// Re-marshal both and compare JSON to verify round-trip equality.
			wantJSON, _ := json.Marshal(tc.msg)
			gotJSON, _ := json.Marshal(got)

			if string(wantJSON) != string(gotJSON) {
				t.Errorf("round-trip mismatch for %s:\n  want: %s\n  got:  %s", tc.name, wantJSON, gotJSON)
			}
		})
	}
}

func TestDonePayload_QualityGatePassed_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		passed bool
	}{
		{"gate_passed", true},
		{"gate_failed", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			msg := protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            "bead-qg",
					WorkerID:          "worker-qg",
					QualityGatePassed: tc.passed,
				},
			}

			data, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var got protocol.Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got.Done == nil {
				t.Fatal("expected Done payload to be non-nil")
			}
			if got.Done.QualityGatePassed != tc.passed {
				t.Errorf("QualityGatePassed: got %v, want %v", got.Done.QualityGatePassed, tc.passed)
			}
		})
	}
}

func TestValidateBeadID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		beadID  string
		wantErr bool
	}{
		// Valid IDs
		{"valid_simple", "oro-1nf", false},
		{"valid_with_dot", "oro-1nf.1", false},
		{"valid_with_multiple_dots", "oro-3e0.2.1", false},
		{"valid_dfe", "oro-dfe.3", false},
		{"valid_lowercase_letters", "abc-123", false},
		{"valid_with_hyphens", "oro-test-1", false},
		{"valid_with_underscores", "oro_test_1", false},
		{"valid_mixed", "oro-1nf_2.3-test", false},
		{"valid_min_length", "a1", false},
		{"valid_max_length", "a12345678901234567890123456789012345678901234567890123456789012", false}, // 63 chars total

		// Invalid IDs - path traversal
		{"invalid_parent_dir", "../etc", true},
		{"invalid_parent_with_valid_start", "oro-1nf/../etc", true},
		{"invalid_double_parent", "../../etc", true},
		{"invalid_absolute_path", "/etc/passwd", true},
		{"invalid_absolute_with_valid_prefix", "/oro-1nf", true},

		// Invalid IDs - special characters
		{"invalid_backslash", "oro\\test", true},
		{"invalid_null_byte", "oro\x00test", true},
		{"invalid_space", "oro test", true},
		{"invalid_special_chars", "oro@test", true},
		{"invalid_parentheses", "oro(test)", true},
		{"invalid_brackets", "oro[test]", true},
		{"invalid_braces", "oro{test}", true},

		// Invalid IDs - format violations
		{"invalid_empty", "", true},
		{"invalid_too_short", "a", true},
		{"invalid_starts_with_hyphen", "-oro", true},
		{"invalid_starts_with_dot", ".oro", true},
		{"invalid_starts_with_underscore", "_oro", true},
		{"invalid_uppercase", "ORO-1NF", true},
		{"invalid_ends_with_hyphen", "oro-", true},
		{"invalid_too_long", "a1234567890123456789012345678901234567890123456789012345678901234", true}, // 64 chars
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := protocol.ValidateBeadID(tc.beadID)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateBeadID(%q) = nil, want error", tc.beadID)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateBeadID(%q) = %v, want nil", tc.beadID, err)
			}
		})
	}
}
