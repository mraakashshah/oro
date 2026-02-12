package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// newDashCmd creates the "oro dash" subcommand.
func newDashCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dash",
		Short: "Launch interactive dashboard",
		Long:  "Opens the oro dashboard TUI for monitoring beads, workers, and swarm state.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Execute oro-dash binary
			dashCmd := exec.CommandContext(cmd.Context(), "oro-dash")
			dashCmd.Stdin = os.Stdin
			dashCmd.Stdout = os.Stdout
			dashCmd.Stderr = os.Stderr

			if err := dashCmd.Run(); err != nil {
				return fmt.Errorf("run oro-dash: %w", err)
			}

			return nil
		},
	}
}
