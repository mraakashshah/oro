package agentruntime_test

import (
	"reflect"
	"testing"

	"oro/pkg/agentruntime"
	"oro/pkg/protocol"
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

func TestRuntimeInterfaceShape(t *testing.T) {
	runtimeType := reflect.TypeOf((*agentruntime.Runtime)(nil)).Elem()

	expected := map[string]reflect.Type{
		"ID":                          reflect.TypeOf(func() agentruntime.RuntimeID { return "" }),
		"DefaultTierModel":            reflect.TypeOf(func(string, protocol.Tier) string { return "" }),
		"StreamFormat":                reflect.TypeOf(func() agentruntime.StreamFormat { return "" }),
		"InstructionLayout":           reflect.TypeOf(func() agentruntime.InstructionLayout { return agentruntime.InstructionLayout{} }),
		"SupportsHooks":               reflect.TypeOf(func() bool { return false }),
		"SupportsProjectSkillInstall": reflect.TypeOf(func() bool { return false }),
	}

	if runtimeType.NumMethod() != len(expected) {
		t.Fatalf("Runtime has %d methods, want %d", runtimeType.NumMethod(), len(expected))
	}

	for name, want := range expected {
		method, ok := runtimeType.MethodByName(name)
		if !ok {
			t.Fatalf("Runtime missing method %s", name)
		}
		if method.Type != want {
			t.Fatalf("Runtime.%s type = %s, want %s", name, method.Type, want)
		}
	}

	if _, ok := runtimeType.MethodByName("Spawn"); ok {
		t.Fatal("Runtime must not declare Spawn")
	}
}
