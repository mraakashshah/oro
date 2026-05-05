package main

import "github.com/spf13/cobra"

// newCardsCmd creates the "oro cards" command group.
func newCardsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cards",
		Short: "Manage knowledge cards",
		Long:  "Manage durable knowledge cards (rules, patterns, decisions, facts).\nCards are the long-lived knowledge layer that replaces pkg/memory (§5 harness spec).",
	}
	cmd.AddCommand(newImportFromMemoryCmd())
	return cmd
}
