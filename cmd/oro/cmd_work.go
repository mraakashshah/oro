package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"oro/pkg/dispatcher"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// exitCodeBeadError is returned when the bead is not found or invalid.
const exitCodeBeadError = 3

// workConfig holds parsed flags and loaded bead for the work command.
type workConfig struct {
	beadID     string
	model      string
	timeout    time.Duration
	skipReview bool
	resume     bool
	dryRun     bool
	bead       *protocol.BeadDetail
}

// validate checks that the loaded bead has the required fields.
func (c *workConfig) validate() error {
	if c.bead.Title == "" {
		return fmt.Errorf("bead %s has no title", c.bead.ID)
	}
	if c.bead.AcceptanceCriteria == "" {
		return fmt.Errorf("bead %s has no acceptance criteria — add with: bd update %s --acceptance-criteria \"...\"", c.bead.ID, c.bead.ID)
	}
	return nil
}

// newWorkCmd creates the "oro work" subcommand.
func newWorkCmd() *cobra.Command {
	var cfg workConfig

	cmd := &cobra.Command{
		Use:   "work <bead-id>",
		Short: "Execute a bead through the full lifecycle",
		Long: `Drives a single bead end-to-end: worktree → claude → quality gate →
ops review → merge → close. Runnable by a human or a claude agent.

All retries, model escalation, and review feedback loops are handled
automatically. Exit code 0 means the bead landed on main.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.beadID = args[0]
			return runWork(cmd, &cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.model, "model", protocol.DefaultModel, "starting Claude model")
	cmd.Flags().DurationVar(&cfg.timeout, "timeout", 15*time.Minute, "per-claude-spawn timeout")
	cmd.Flags().BoolVar(&cfg.skipReview, "skip-review", false, "skip ops review gate")
	cmd.Flags().BoolVar(&cfg.resume, "resume", false, "resume from existing worktree")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "show execution plan without running")

	return cmd
}

// runWork is the entry point for oro work. It loads the bead, validates it,
// and will orchestrate the full lifecycle in subsequent beads.
func runWork(_ *cobra.Command, cfg *workConfig) error {
	ctx := context.Background()

	// Load bead details.
	runner := &dispatcher.ExecCommandRunner{}
	beadSrc := dispatcher.NewCLIBeadSource(runner)

	detail, err := beadSrc.Show(ctx, cfg.beadID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCodeBeadError)
	}
	cfg.bead = detail

	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCodeBeadError)
	}

	fmt.Fprintf(os.Stderr, "Loaded %s: %s\n", cfg.bead.ID, cfg.bead.Title)

	if cfg.dryRun {
		fmt.Fprintf(os.Stderr, "Dry run — would execute bead %s with model=%s, timeout=%s, skip-review=%t, resume=%t\n",
			cfg.beadID, cfg.model, cfg.timeout, cfg.skipReview, cfg.resume)
		return nil
	}

	// Orchestration continues in oro-mtuf.3+
	return nil
}
