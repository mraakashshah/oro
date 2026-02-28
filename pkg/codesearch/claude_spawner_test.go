package codesearch_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/codesearch"
)

// TestClaudeSpawner_ExtractsResultFromEnvelope verifies that ExtractResultFromEnvelope
// parses the JSON envelope produced by claude -p --output-format json, extracts the
// "result" field, and strips markdown code fences.
func TestClaudeSpawner_ExtractsResultFromEnvelope(t *testing.T) {
	inner := `[{"id": "9", "reason": "most relevant"}]`
	// Build a realistic Claude --output-format json envelope using json.Marshal
	// so that newlines and backticks are correctly escaped in the JSON output.
	type envelopeShape struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	e := envelopeShape{
		Type:    "result",
		Subtype: "success",
		Result:  "```json\n" + inner + "\n```",
		IsError: false,
	}
	envelopeJSON, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	got, err := codesearch.ExtractResultFromEnvelope(envelopeJSON)
	if err != nil {
		t.Fatalf("ExtractResultFromEnvelope: %v", err)
	}
	if got != inner {
		t.Errorf("got %q, want %q", got, inner)
	}
}

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
