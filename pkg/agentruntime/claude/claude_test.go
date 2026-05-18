package claude_test

import (
	"reflect"
	"testing"

	"oro/pkg/agentruntime"
	"oro/pkg/agentruntime/claude"
	"oro/pkg/protocol"
)

func TestClaudeRuntimeImplementsInterface(t *testing.T) {
	var _ agentruntime.Runtime = (*claude.Runtime)(nil)
}

func TestClaudeRuntimeDescriptors(t *testing.T) {
	runtime := claude.New()

	if got := runtime.ID(); got != agentruntime.RuntimeIDClaude {
		t.Fatalf("ID() = %q, want %q", got, agentruntime.RuntimeIDClaude)
	}
	if got := runtime.StreamFormat(); got != agentruntime.StreamFormatClaudeJSON {
		t.Fatalf("StreamFormat() = %q, want %q", got, agentruntime.StreamFormatClaudeJSON)
	}
	if !runtime.SupportsHooks() {
		t.Fatal("SupportsHooks() = false, want true")
	}
	if !runtime.SupportsProjectSkillInstall() {
		t.Fatal("SupportsProjectSkillInstall() = false, want true")
	}
	for _, tc := range []struct {
		role string
		tier protocol.Tier
	}{
		{role: "", tier: ""},
		{role: "worker", tier: protocol.TierFast},
		{role: "spec_writer", tier: protocol.TierDeep},
	} {
		if got := runtime.DefaultTierModel(tc.role, tc.tier); got != "" {
			t.Fatalf("DefaultTierModel(%q, %q) = %q, want empty string", tc.role, tc.tier, got)
		}
	}
	if got := runtime.InstructionLayout(); !reflect.DeepEqual(got, agentruntime.InstructionLayout{}) {
		t.Fatalf("InstructionLayout() = %#v, want zero value", got)
	}
}
