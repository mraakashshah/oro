package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Oro version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), root.Version)
			return nil
		},
	}
}
