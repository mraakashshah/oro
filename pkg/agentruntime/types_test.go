package agentruntime_test

import (
	"reflect"
	"testing"

	"oro/pkg/agentruntime"
)

func TestRuntimeIDValues(t *testing.T) {
	if got := agentruntime.RuntimeIDClaude; got != agentruntime.RuntimeID("claude") {
		t.Fatalf("RuntimeIDClaude = %q, want claude", got)
	}
	if got := agentruntime.RuntimeIDCodex; got != agentruntime.RuntimeID("codex") {
		t.Fatalf("RuntimeIDCodex = %q, want codex", got)
	}
}

func TestInstructionLayoutZeroValue(t *testing.T) {
	var layout agentruntime.InstructionLayout
	if layout.Workdir != "" {
		t.Fatalf("zero Workdir = %q, want empty", layout.Workdir)
	}
	if layout.ExtraPaths != nil {
		t.Fatalf("zero ExtraPaths = %#v, want nil", layout.ExtraPaths)
	}
	typ := reflect.TypeOf(layout)
	if _, ok := typ.FieldByName("Workdir"); !ok {
		t.Fatal("InstructionLayout missing Workdir field")
	}
	if _, ok := typ.FieldByName("ExtraPaths"); !ok {
		t.Fatal("InstructionLayout missing ExtraPaths field")
	}
}

func TestStreamFormatValues(t *testing.T) {
	if got := agentruntime.StreamFormatClaudeJSON; got != agentruntime.StreamFormat("claude_stream_json") {
		t.Fatalf("StreamFormatClaudeJSON = %q, want claude_stream_json", got)
	}
	if got := agentruntime.StreamFormatLineText; got != agentruntime.StreamFormat("line_text") {
		t.Fatalf("StreamFormatLineText = %q, want line_text", got)
	}
}
