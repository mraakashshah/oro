package main

import (
	"encoding/json"
	"fmt"

	"oro/pkg/janitor"

	"github.com/spf13/cobra"
)

func newJanitorDetectCmd() *cobra.Command {
	var detector, targetBranch string
	cmd := &cobra.Command{
		Use:    "janitor:detect --detector <name>",
		Short:  "Rerun one built-in janitor detector",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			candidates, err := janitor.RunBuiltin(cmd.Context(), currentRepoRoot(), targetBranch, detector)
			if err != nil {
				return fmt.Errorf("run janitor detector: %w", err)
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(candidates); err != nil {
				return fmt.Errorf("write janitor detector result: %w", err)
			}
			if len(candidates) > 0 {
				return fmt.Errorf("janitor detector %q found %d candidate(s)", detector, len(candidates))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&detector, "detector", "", "built-in detector name")
	cmd.Flags().StringVar(&targetBranch, "target-branch", "", "branch inspected by the CI detector")
	_ = cmd.MarkFlagRequired("detector")
	return cmd
}
