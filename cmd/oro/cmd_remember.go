package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newRememberCmd creates the "oro remember" subcommand.
func newRememberCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remember <text>",
		Short: "Store a memory",
		Long:  "Insert a memory into the store. Supports type hints via prefix\n(lesson:, decision:, gotcha:, pattern:). Default type: lesson.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args, " ")
			fmt.Fprintf(cmd.OutOrStdout(), "not implemented: remember %q\n", text)
			return nil
		},
	}
}
