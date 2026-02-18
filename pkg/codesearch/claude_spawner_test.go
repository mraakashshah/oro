package codesearch_test

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/codesearch"
)

// TestSpawnCmdSetup verifies that BuildCmd constructs an exec.Cmd with:
//  1. Stdin set to a non-nil reader (prevents claude -p from hanging in non-TTY contexts)
//  2. No env var with CLAUDECODE prefix in cmd.Env (prevents altered spawned-claude behavior)
func TestSpawnCmdSetup(t *testing.T) {
	ctx := context.Background()
	cmd := codesearch.BuildCmd(ctx, "test prompt")

	if cmd.Stdin == nil {
		t.Error("cmd.Stdin must be non-nil: claude -p hangs indefinitely in non-TTY contexts when stdin is nil")
	}

	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "CLAUDECODE") {
			t.Errorf("cmd.Env must not contain CLAUDECODE* vars, got: %s", env)
		}
	}
}
