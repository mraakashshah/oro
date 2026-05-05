package main

import (
	"encoding/json"
	"fmt"
	"io"

	"oro/pkg/dispatcher"

	"github.com/spf13/cobra"
)

// newTestContextSafetyCmd returns the `oro test:context-safety` command.
// It resolves the effective warning/checkpoint thresholds from (lowest→highest
// priority): oro.toml config → per-bead JSON override → --threshold-override
// CLI flag, and prints the resolved values. Intended for diagnostics and
// automated testing of §9.4 threshold configuration.
func newTestContextSafetyCmd() *cobra.Command {
	var (
		thresholdOverride float64
		beadThresholds    string
		configPath        string
	)

	cmd := &cobra.Command{
		Use:   "test:context-safety",
		Short: "Report effective context-safety thresholds (diagnostic)",
		Long: `Resolves and prints the effective warning/checkpoint thresholds for context
safety (§9.4). Priority (highest wins): --threshold-override > per-bead
context_thresholds JSON > oro.toml [dispatcher.context_safety] > defaults.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTestContextSafety(cmd.OutOrStdout(), configPath, beadThresholds, thresholdOverride)
		},
	}

	cmd.Flags().Float64Var(&thresholdOverride, "threshold-override", 0, "override warning threshold for this invocation (wins over config and per-bead values)")
	cmd.Flags().StringVar(&beadThresholds, "bead-thresholds", "", `per-bead context_thresholds JSON, e.g. '{"warning":0.70,"checkpoint":0.80}'`)
	cmd.Flags().StringVar(&configPath, "config", "", "path to oro.toml (default: auto-detect)")

	return cmd
}

func runTestContextSafety(w io.Writer, configPath, beadThresholdsJSON string, thresholdOverride float64) error {
	// 1. Load base thresholds from config.
	cfg, err := dispatcher.LoadContextSafetyConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	warning := cfg.WarningThreshold
	checkpoint := cfg.CheckpointThreshold

	// 2. Apply per-bead override if provided.
	if beadThresholdsJSON != "" {
		var ov struct {
			Warning    float64 `json:"warning"`
			Checkpoint float64 `json:"checkpoint"`
		}
		if err := json.Unmarshal([]byte(beadThresholdsJSON), &ov); err != nil {
			return fmt.Errorf("parse --bead-thresholds: %w", err)
		}
		if ov.Warning > 0 {
			warning = ov.Warning
		}
		if ov.Checkpoint > 0 {
			checkpoint = ov.Checkpoint
		}
	}

	// 3. CLI --threshold-override wins over everything.
	if thresholdOverride > 0 {
		warning = thresholdOverride
	}

	fmt.Fprintf(w, "warning_threshold=%.2f\n", warning)
	fmt.Fprintf(w, "checkpoint_threshold=%.2f\n", checkpoint)
	return nil
}
