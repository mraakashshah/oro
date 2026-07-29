package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// inheritedOroRuntimeVars are transient ORO_* runtime vars that must not leak
// from the caller's environment into cmd/oro tests. A factory worker runs
// `go test ./cmd/oro` with ORO_WORKER_ID/ORO_SOCKET_PATH/etc. exported; without
// sanitization those bleed into tests that resolve runtime state.
//
// ORO_HOME is intentionally excluded: TestMain sets it to a temp dir explicitly
// for hermeticity (so tests never resolve to the developer's real ~/.oro).
var inheritedOroRuntimeVars = []string{
	"ORO_CAPABILITY_FILE",
	"ORO_PROJECT",
	"ORO_SOCKET_PATH",
	"ORO_TMUX_MANAGED_DAEMON",
	"ORO_WORKER",
	"ORO_WORKER_ID",
	"ORO_WORKER_BEAD_ID",
}

// sanitizeInheritedOroEnv unsets the transient ORO_* runtime vars so inherited
// caller state cannot influence cmd/oro tests. It is called from TestMain.
func sanitizeInheritedOroEnv() error {
	for _, key := range inheritedOroRuntimeVars {
		if err := os.Unsetenv(key); err != nil {
			return fmt.Errorf("unset %s: %w", key, err)
		}
	}
	return nil
}

func TestSanitizeInheritedOroEnv(t *testing.T) {
	t.Setenv("ORO_WORKER", "1")
	t.Setenv("ORO_WORKER_ID", "leaked-worker")
	t.Setenv("ORO_PROJECT", "leaked-project")
	t.Setenv("ORO_SOCKET_PATH", "/tmp/leaked.sock")
	t.Setenv("ORO_CAPABILITY_FILE", "/tmp/leaked-capability.json")

	if err := sanitizeInheritedOroEnv(); err != nil {
		t.Fatalf("sanitizeInheritedOroEnv: %v", err)
	}
	for _, key := range inheritedOroRuntimeVars {
		if v, ok := os.LookupEnv(key); ok {
			t.Errorf("%s still set after sanitize: %q", key, v)
		}
	}
}

// TestInheritedOroRuntimeEnvSanitized proves the package test environment is both
// SANITIZED (no inherited transient ORO_* vars) and HERMETIC (ORO_HOME resolves to
// a temp dir, never the real ~/.oro). The hermetic half guards against the
// regression that rejected oro-5wbj: preserving real HOME + unsetting ORO_HOME
// makes non-isolating tests write into the developer's real ~/.oro.
func TestInheritedOroRuntimeEnvSanitized(t *testing.T) {
	for _, key := range inheritedOroRuntimeVars {
		if v, ok := os.LookupEnv(key); ok {
			t.Errorf("TestMain preserved inherited %s=%q", key, v)
		}
	}

	home, err := resolveOroHome()
	if err != nil {
		t.Fatalf("resolveOroHome: %v", err)
	}
	if !strings.Contains(home, "oro-manager-runtime-test") {
		t.Errorf("resolveOroHome() = %q, want a temp oro-home (hermetic); real ~/.oro pollution risk", home)
	}

	// Explicit per-test overrides must still take effect.
	t.Setenv("ORO_PROJECT", "explicit-test-project")
	if got := os.Getenv("ORO_PROJECT"); got != "explicit-test-project" {
		t.Fatalf("explicit t.Setenv did not take effect: got %q", got)
	}
}
