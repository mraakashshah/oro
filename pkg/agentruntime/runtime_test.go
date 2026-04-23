package agentruntime_test

import (
	"testing"

	"oro/pkg/agentruntime"
)

func TestReadRuntimeDefaultsToClaude(t *testing.T) {
	t.Setenv(agentruntime.EnvVar, "")

	if got := agentruntime.ReadRuntime(); got != agentruntime.RuntimeClaude {
		t.Fatalf("ReadRuntime() = %q, want %q", got, agentruntime.RuntimeClaude)
	}
}

func TestReadRuntimeNormalizesConfiguredValue(t *testing.T) {
	t.Setenv(agentruntime.EnvVar, " CoDeX ")

	if got := agentruntime.ReadRuntime(); got != agentruntime.RuntimeCodex {
		t.Fatalf("ReadRuntime() = %q, want %q", got, agentruntime.RuntimeCodex)
	}
}
