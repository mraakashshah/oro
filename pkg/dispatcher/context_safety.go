package dispatcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"oro/pkg/protocol"

	"github.com/pelletier/go-toml/v2"
)

// Default context-safety thresholds (§9.4).
const (
	defaultWarningThreshold    = 0.65
	defaultCheckpointThreshold = 0.75
)

// ContextSafetyConfig holds the configurable context-usage thresholds (§9.4).
// Both fields are expressed as fractions in [0, 1].
type ContextSafetyConfig struct {
	WarningThreshold    float64 `toml:"warning_threshold"`
	CheckpointThreshold float64 `toml:"checkpoint_threshold"`
}

// LoadContextSafetyConfig reads [dispatcher.context_safety] from an oro.toml
// file. Missing file or missing keys fall back to the documented defaults.
func LoadContextSafetyConfig(path string) (ContextSafetyConfig, error) {
	defaults := ContextSafetyConfig{
		WarningThreshold:    defaultWarningThreshold,
		CheckpointThreshold: defaultCheckpointThreshold,
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is an explicit user-provided config file, not untrusted input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}
		return defaults, fmt.Errorf("load context safety config: %w", err)
	}

	var raw struct {
		Dispatcher struct {
			ContextSafety ContextSafetyConfig `toml:"context_safety"`
		} `toml:"dispatcher"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return defaults, fmt.Errorf("parse context safety config: %w", err)
	}

	out := raw.Dispatcher.ContextSafety
	if out.WarningThreshold == 0 {
		out.WarningThreshold = defaultWarningThreshold
	}
	if out.CheckpointThreshold == 0 {
		out.CheckpointThreshold = defaultCheckpointThreshold
	}
	return out, nil
}

// beadThresholdOverride is the JSON shape stored in bead.context_thresholds (§9.4).
type beadThresholdOverride struct {
	Warning    float64 `json:"warning"`
	Checkpoint float64 `json:"checkpoint"`
}

// thresholdsForBead resolves the effective (warning, checkpoint) pair for a
// bead. Per-bead JSON overrides the dispatcher config; malformed JSON falls
// back to config defaults and emits a warning to stderr.
func (d *Dispatcher) thresholdsForBead(bead protocol.Bead) (warning, checkpoint float64) {
	warning = d.cfg.ContextSafety.WarningThreshold
	checkpoint = d.cfg.ContextSafety.CheckpointThreshold

	if warning == 0 {
		warning = defaultWarningThreshold
	}
	if checkpoint == 0 {
		checkpoint = defaultCheckpointThreshold
	}

	if bead.ContextThresholds == "" {
		return warning, checkpoint
	}

	var ov beadThresholdOverride
	if err := json.Unmarshal([]byte(bead.ContextThresholds), &ov); err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: context_thresholds malformed JSON for bead %s, using config defaults: %v\n", bead.ID, err)
		return warning, checkpoint
	}

	if ov.Warning > 0 {
		warning = ov.Warning
	}
	if ov.Checkpoint > 0 {
		checkpoint = ov.Checkpoint
	}
	return warning, checkpoint
}
