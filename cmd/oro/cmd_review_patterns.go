package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newReviewPatternsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-patterns",
		Short: "Manage review patterns",
		Long:  "Commands for managing ops review patterns and candidate inboxes.",
	}
	cmd.AddCommand(newReviewPatternsCandidatesCmd())
	return cmd
}

func newReviewPatternsCandidatesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "candidates",
		Short: "Show the review pattern candidates inbox",
		Long:  "Prints the path and content of the review pattern candidates inbox file. If the file does not exist, prints the path and reports no candidates.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			paths, err := ResolvePaths(repoRoot)
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			candidatePath := paths.ReviewPatternCandidates
			fmt.Fprintf(cmd.OutOrStdout(), "Candidate inbox: %s\n\n", candidatePath)
			data, err := os.ReadFile(candidatePath) //nolint:gosec // path from ResolvePaths (trusted)
			if os.IsNotExist(err) {
				fmt.Fprintln(cmd.OutOrStdout(), "(no candidates)")
				return nil
			}
			if err != nil {
				return fmt.Errorf("read candidates: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
}
