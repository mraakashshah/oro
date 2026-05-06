package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newReviewPatternsCmd creates the "oro review-patterns" command group.
func newReviewPatternsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-patterns",
		Short: "Manage review pattern candidates",
		Long:  "Inspect and promote candidate review patterns captured from approved reviews.",
	}
	cmd.AddCommand(newReviewPatternsCandidatesCmd())
	cmd.AddCommand(newReviewPatternsPromoteCmd())
	return cmd
}

func newReviewPatternsCandidatesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "candidates",
		Short: "Show candidate review patterns from the inbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			paths, err := ResolvePaths(cwd)
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Candidate inbox: %s\n\n", paths.ReviewPatternCandidates)
			data, err := os.ReadFile(paths.ReviewPatternCandidates) //nolint:gosec // path from trusted ResolvePaths
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "(no candidates)")
					return nil
				}
				return fmt.Errorf("read candidates: %w", err)
			}
			_, _ = cmd.OutOrStdout().Write(data)
			return nil
		},
	}
}

func newReviewPatternsPromoteCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote candidate patterns to the curated review-patterns file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !all {
				return fmt.Errorf("use --all to promote all candidates")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			paths, err := ResolvePaths(cwd)
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			n, err := promoteReviewPatternCandidates(paths.ReviewPatternCandidates, paths.ReviewPatterns)
			if err != nil {
				return fmt.Errorf("promote: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Promoted %d pattern(s)\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "promote all candidates")
	return cmd
}

// promoteReviewPatternCandidates reads candidate patterns from candidatePath,
// appends only those absent from curatedPath to curatedPath, and returns the
// number of patterns promoted. Creates curatedPath (and its parent directory)
// if it does not exist.
func promoteReviewPatternCandidates(candidatePath, curatedPath string) (int, error) {
	candidates, err := parseCandidatePatterns(candidatePath)
	if err != nil {
		return 0, fmt.Errorf("parse candidates: %w", err)
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	if err := os.MkdirAll(filepath.Dir(curatedPath), 0o750); err != nil {
		return 0, fmt.Errorf("create curated dir: %w", err)
	}

	existingRaw, err := os.ReadFile(curatedPath) //nolint:gosec // curatedPath from trusted caller
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("read curated patterns: %w", err)
	}
	existing := string(existingRaw)

	f, err := os.OpenFile(curatedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // curatedPath from trusted caller
	if err != nil {
		return 0, fmt.Errorf("open curated file: %w", err)
	}
	defer f.Close()

	promoted := 0
	for _, c := range candidates {
		normalized := strings.TrimSpace(c)
		if normalized == "" || strings.Contains(existing, normalized) {
			continue
		}
		if _, err := fmt.Fprintf(f, "%s\n", normalized); err != nil {
			return promoted, fmt.Errorf("write pattern: %w", err)
		}
		existing += normalized // guard against duplicates within this run
		promoted++
	}
	return promoted, nil
}

// parseCandidatePatterns extracts pattern texts from a candidate inbox file.
// Each record is a "---" separated block: header fields, a blank line, then the
// pattern text.
func parseCandidatePatterns(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path from trusted caller
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read candidates file: %w", err)
	}
	return extractPatternsFromCandidateFile(string(data)), nil
}

// extractPatternsFromCandidateFile parses the pattern text from each "---"
// delimited record written by appendReviewPatternCandidates.
func extractPatternsFromCandidateFile(content string) []string {
	var patterns []string
	for _, block := range strings.Split(content, "---\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		// Header and pattern are separated by a blank line.
		idx := strings.Index(block, "\n\n")
		if idx < 0 {
			continue
		}
		if p := strings.TrimSpace(block[idx+2:]); p != "" {
			patterns = append(patterns, p)
		}
	}
	return patterns
}
