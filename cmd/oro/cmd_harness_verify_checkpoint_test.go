package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type mockCheckpointRunner struct {
	passed bool
	output string
	err    error
}

func (m *mockCheckpointRunner) run(_ context.Context) (bool, string, error) {
	return m.passed, m.output, m.err
}

func TestVerifyCheckpointCLIWraps(t *testing.T) {
	t.Run("pass_outputs_section_marker_and_PASS", func(t *testing.T) {
		runner := &mockCheckpointRunner{
			passed: true,
			output: "=== RUN   TestCheckpointE2EFromHighContext\n--- PASS: TestCheckpointE2EFromHighContext (1.23s)\n",
		}
		cmd := newHarnessVerifyCheckpointCmdWithRunner(runner)
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected exit 0 on pass, got error: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, "§18.3") {
			t.Errorf("stdout missing §18.3 section marker: %q", out)
		}
		if !strings.Contains(out, "PASS") {
			t.Errorf("stdout missing PASS marker: %q", out)
		}
	})

	t.Run("regression_guard_missing_checkpoint_received_exits_nonzero", func(t *testing.T) {
		// Simulate the E2E test failing because checkpoint_acked (CHECKPOINT_RECEIVED)
		// is absent from the events table — the regression this guards against.
		runner := &mockCheckpointRunner{
			passed: false,
			output: "§18.3 event chain: event \"checkpoint_acked\" missing or out of order\n--- FAIL: TestCheckpointE2EFromHighContext\n",
		}
		cmd := newHarnessVerifyCheckpointCmdWithRunner(runner)
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		if err := cmd.Execute(); err == nil {
			t.Fatal("expected non-zero exit when CHECKPOINT_RECEIVED event missing, got nil error")
		}
		out := stdout.String()
		if !strings.Contains(out, "§18.3") {
			t.Errorf("stdout missing §18.3 section marker: %q", out)
		}
		if !strings.Contains(out, "FAIL") {
			t.Errorf("stdout missing FAIL marker: %q", out)
		}
	})
}
