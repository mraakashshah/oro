package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContextSafetyCLIOverride verifies §9.4: --threshold-override wins over
// both the oro.toml config value and any per-bead context_thresholds value.
func TestContextSafetyCLIOverride(t *testing.T) {
	t.Parallel()

	// Fixture: config with warning_threshold=0.65 (different from CLI override).
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "oro.toml")
	cfgContent := "[dispatcher.context_safety]\nwarning_threshold = 0.65\ncheckpoint_threshold = 0.75\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fixture: per-bead thresholds (also different from CLI override).
	beadThresholds := `{"warning":0.70,"checkpoint":0.80}`

	var out bytes.Buffer
	cmd := newTestContextSafetyCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", cfgPath,
		"--bead-thresholds", beadThresholds,
		"--threshold-override", "0.55",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("test:context-safety: %v\noutput: %s", err, out.String())
	}

	got := out.String()
	// CLI override must win — output must reflect 0.55, not config (0.65) or bead (0.70).
	if !strings.Contains(got, "warning_threshold=0.55") {
		t.Errorf("expected warning_threshold=0.55 in output (CLI override wins), got:\n%s", got)
	}
}
