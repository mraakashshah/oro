package worker_test

import (
	"strings"
	"testing"

	"oro/pkg/worker"
)

func TestParseStreamEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantKind worker.ActivityKind
		wantTool string
		wantText string
		wantErr  bool
	}{
		{
			name:     "tool_use content block",
			input:    `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"toolu_01","input":{"file_path":"/foo"}}]}}`,
			wantKind: worker.ActivityToolUse,
			wantTool: "Read",
		},
		{
			name:     "text content block",
			input:    `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`,
			wantKind: worker.ActivityTextDelta,
			wantText: "Hello world",
		},
		{
			name:     "text with newlines",
			input:    `{"type":"assistant","message":{"content":[{"type":"text","text":"line one\nline two\n"}]}}`,
			wantKind: worker.ActivityTextDelta,
			wantText: "line one\nline two\n",
		},
		{
			name:     "result success",
			input:    `{"type":"result","subtype":"success","result":"Final output","is_error":false,"num_turns":3,"stop_reason":"end_turn","duration_ms":5000,"total_cost_usd":0.005,"permission_denials":[]}`,
			wantKind: worker.ActivityResult,
			wantText: "Final output",
			wantErr:  false,
		},
		{
			name:     "result error",
			input:    `{"type":"result","result":"something failed","is_error":true}`,
			wantKind: worker.ActivityResult,
			wantText: "something failed",
			wantErr:  true,
		},
		{
			name:     "tool_use prioritized over text",
			input:    `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me read"},{"type":"tool_use","name":"Bash","id":"toolu_02","input":{}}]}}`,
			wantKind: worker.ActivityToolUse,
			wantTool: "Bash",
		},
		{
			name:     "system event is unknown",
			input:    `{"type":"system","subtype":"init","session_id":"abc"}`,
			wantKind: worker.ActivityUnknown,
		},
		{
			name:     "empty content array",
			input:    `{"type":"assistant","message":{"content":[]}}`,
			wantKind: worker.ActivityUnknown,
		},
		{
			name:     "malformed JSON",
			input:    `{not valid json}`,
			wantKind: worker.ActivityUnknown,
		},
		{
			name:     "empty line",
			input:    ``,
			wantKind: worker.ActivityUnknown,
		},
		{
			name:     "unknown type field",
			input:    `{"type":"heartbeat","data":"test"}`,
			wantKind: worker.ActivityUnknown,
		},
		{
			name:     "missing message field",
			input:    `{"type":"assistant"}`,
			wantKind: worker.ActivityUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := worker.ParseStreamEvent([]byte(tt.input))

			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %d, want %d", got.Kind, tt.wantKind)
			}
			if got.Tool != tt.wantTool {
				t.Errorf("Tool = %q, want %q", got.Tool, tt.wantTool)
			}
			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if got.IsError != tt.wantErr {
				t.Errorf("IsError = %v, want %v", got.IsError, tt.wantErr)
			}
		})
	}
}

func TestParseStreamEvent_ResultMetadata(t *testing.T) {
	t.Parallel()

	input := `{"type":"result","subtype":"success","result":"done","is_error":false,"num_turns":5,"stop_reason":"end_turn","duration_ms":8000,"total_cost_usd":0.0042,"permission_denials":["Read","Bash"]}`
	got := worker.ParseStreamEvent([]byte(input))

	if got.NumTurns != 5 {
		t.Errorf("NumTurns = %d, want 5", got.NumTurns)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", got.StopReason, "end_turn")
	}
	if got.DurationMs != 8000 {
		t.Errorf("DurationMs = %d, want 8000", got.DurationMs)
	}
	if got.CostUSD != 0.0042 {
		t.Errorf("CostUSD = %f, want 0.0042", got.CostUSD)
	}
	if len(got.PermissionDenials) != 2 || got.PermissionDenials[0] != "Read" {
		t.Errorf("PermissionDenials = %v, want [Read Bash]", got.PermissionDenials)
	}
	if got.ResultSubtype != "success" {
		t.Errorf("ResultSubtype = %q, want %q", got.ResultSubtype, "success")
	}
}

func TestFormatResult(t *testing.T) {
	t.Parallel()

	t.Run("success with metadata", func(t *testing.T) {
		t.Parallel()
		a := worker.Activity{
			Kind:          worker.ActivityResult,
			ResultSubtype: "success",
			NumTurns:      3,
			DurationMs:    5000,
			CostUSD:       0.005,
			StopReason:    "end_turn",
		}
		got := worker.FormatResult(a)
		if got == "" {
			t.Fatal("expected non-empty result summary")
		}
		if !strings.Contains(got, "turns=3") {
			t.Errorf("missing turns in %q", got)
		}
	})

	t.Run("with permission denials", func(t *testing.T) {
		t.Parallel()
		a := worker.Activity{
			Kind:              worker.ActivityResult,
			ResultSubtype:     "success",
			PermissionDenials: []string{"Read", "Bash"},
		}
		got := worker.FormatResult(a)
		if !strings.Contains(got, "PERMISSION DENIALS") {
			t.Errorf("missing permission denials in %q", got)
		}
	})

	t.Run("non-result returns empty", func(t *testing.T) {
		t.Parallel()
		a := worker.Activity{Kind: worker.ActivityToolUse, Tool: "Read"}
		if got := worker.FormatResult(a); got != "" {
			t.Errorf("expected empty for non-result, got %q", got)
		}
	})
}

func TestFormatActivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		activity worker.Activity
		want     string
	}{
		{
			name:     "tool use formatted",
			activity: worker.Activity{Kind: worker.ActivityToolUse, Tool: "Read"},
			want:     "-> Read",
		},
		{
			name:     "bash tool use",
			activity: worker.Activity{Kind: worker.ActivityToolUse, Tool: "Bash"},
			want:     "-> Bash",
		},
		{
			name:     "text delta returns empty",
			activity: worker.Activity{Kind: worker.ActivityTextDelta, Text: "hello"},
			want:     "",
		},
		{
			name:     "result returns empty",
			activity: worker.Activity{Kind: worker.ActivityResult, Text: "done"},
			want:     "",
		},
		{
			name:     "unknown returns empty",
			activity: worker.Activity{Kind: worker.ActivityUnknown},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := worker.FormatActivity(tt.activity)
			if got != tt.want {
				t.Errorf("FormatActivity() = %q, want %q", got, tt.want)
			}
		})
	}
}
