package dispatcher //nolint:testpackage // white-box: needs access to unexported dispatcher fields

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/protocol"
)

// TestThresholdsLoadedFromConfig verifies §9.4: dispatcher.LoadConfig reads
// [dispatcher.context_safety] from an oro.toml and falls back to documented defaults.
func TestThresholdsLoadedFromConfig(t *testing.T) {
	t.Parallel()

	t.Run("explicit values parsed from toml", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "oro.toml")
		content := "[dispatcher.context_safety]\nwarning_threshold = 0.65\ncheckpoint_threshold = 0.75\n"
		if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadContextSafetyConfig(cfgPath)
		if err != nil {
			t.Fatalf("LoadContextSafetyConfig: %v", err)
		}

		if cfg.WarningThreshold != 0.65 {
			t.Errorf("WarningThreshold: want 0.65, got %f", cfg.WarningThreshold)
		}
		if cfg.CheckpointThreshold != 0.75 {
			t.Errorf("CheckpointThreshold: want 0.75, got %f", cfg.CheckpointThreshold)
		}
	})

	t.Run("missing keys fall back to defaults", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "oro.toml")
		// [dispatcher] section with no context_safety subsection
		if err := os.WriteFile(cfgPath, []byte("[dispatcher]\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadContextSafetyConfig(cfgPath)
		if err != nil {
			t.Fatalf("LoadContextSafetyConfig: %v", err)
		}

		if cfg.WarningThreshold != defaultWarningThreshold {
			t.Errorf("WarningThreshold: want %f (default), got %f", defaultWarningThreshold, cfg.WarningThreshold)
		}
		if cfg.CheckpointThreshold != defaultCheckpointThreshold {
			t.Errorf("CheckpointThreshold: want %f (default), got %f", defaultCheckpointThreshold, cfg.CheckpointThreshold)
		}
	})

	t.Run("missing file falls back to defaults", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadContextSafetyConfig("/nonexistent/oro.toml")
		if err != nil {
			t.Fatalf("LoadContextSafetyConfig on missing file: %v", err)
		}

		if cfg.WarningThreshold != defaultWarningThreshold {
			t.Errorf("WarningThreshold: want %f (default), got %f", defaultWarningThreshold, cfg.WarningThreshold)
		}
		if cfg.CheckpointThreshold != defaultCheckpointThreshold {
			t.Errorf("CheckpointThreshold: want %f (default), got %f", defaultCheckpointThreshold, cfg.CheckpointThreshold)
		}
	})
}

// TestPerBeadOverrideRespected verifies §9.4: per-bead context_thresholds JSON
// overrides the dispatcher config defaults, with graceful handling of malformed JSON.
func TestPerBeadOverrideRespected(t *testing.T) {
	t.Parallel()

	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.ContextSafety = ContextSafetyConfig{
		WarningThreshold:    0.65,
		CheckpointThreshold: 0.75,
	}

	t.Run("per-bead override wins over config", func(t *testing.T) {
		t.Parallel()
		bead := protocol.Bead{
			ID:                "oro-override1",
			ContextThresholds: `{"warning":0.55,"checkpoint":0.70}`,
		}
		w, c := d.thresholdsForBead(bead)
		if w != 0.55 {
			t.Errorf("warning: want 0.55, got %f", w)
		}
		if c != 0.70 {
			t.Errorf("checkpoint: want 0.70, got %f", c)
		}
	})

	t.Run("bead without column returns config defaults", func(t *testing.T) {
		t.Parallel()
		bead := protocol.Bead{ID: "oro-nooverride1"}
		w, c := d.thresholdsForBead(bead)
		if w != 0.65 {
			t.Errorf("warning: want 0.65 (config default), got %f", w)
		}
		if c != 0.75 {
			t.Errorf("checkpoint: want 0.75 (config default), got %f", c)
		}
	})

	t.Run("malformed JSON logs warning and falls back to config defaults without panic", func(t *testing.T) {
		t.Parallel()
		bead := protocol.Bead{
			ID:                "oro-malformed1",
			ContextThresholds: `{not valid json`,
		}
		// must not panic; should return config defaults
		w, c := d.thresholdsForBead(bead)
		if w != 0.65 {
			t.Errorf("warning: want 0.65 (config default after malformed JSON), got %f", w)
		}
		if c != 0.75 {
			t.Errorf("checkpoint: want 0.75 (config default after malformed JSON), got %f", c)
		}
	})
}
