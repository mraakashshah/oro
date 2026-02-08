package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newRecallCmd creates the "oro recall" subcommand.
func newRecallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recall <query>",
		Short: "Search memories",
		Long:  "Search the memory store by text query.\nDisplays top 5 results with type, content, age, confidence, and source.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			fmt.Fprintf(cmd.OutOrStdout(), "not implemented: recall %q\n", query)
			return nil
		},
	}
}
