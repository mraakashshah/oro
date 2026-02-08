package main

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/memory"

	"github.com/spf13/cobra"
)

// parseTypePrefix extracts a type hint prefix from the text.
// Returns (memoryType, remainingText). If no prefix matches, returns
// ("self_report", original text).
//
//nolint:gocritic // unnamed results are clear from doc comment
func parseTypePrefix(text string) (string, string) {
	//nolint:gochecknoglobals // local-scope workaround: defined inline
	prefixes := map[string]string{
		"lesson:":   "lesson",
		"decision:": "decision",
		"gotcha:":   "gotcha",
		"pattern:":  "pattern",
	}
	for prefix, memType := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return memType, strings.TrimSpace(strings.TrimPrefix(text, prefix))
		}
	}
	return "self_report", text
}

// newRememberCmdWithStore creates the "oro remember" subcommand wired to a memory.Store.
func newRememberCmdWithStore(store *memory.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "remember <text>",
		Short: "Store a memory",
		Long:  "Insert a memory into the store. Supports type hints via prefix\n(lesson:, decision:, gotcha:, pattern:). Default type: self_report.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args, " ")
			memType, content := parseTypePrefix(text)

			id, err := store.Insert(context.Background(), memory.InsertParams{
				Content:    content,
				Type:       memType,
				Source:     "cli",
				Confidence: 0.8,
			})
			if err != nil {
				return fmt.Errorf("remember: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Remembered (id=%d, type=%s): %s\n", id, memType, content)
			return nil
		},
	}
}

// newRememberCmd creates the "oro remember" subcommand.
// Without a store, it prints a stub message. The root command wires this up;
// in production the store is created from the default DB path.
func newRememberCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remember <text>",
		Short: "Store a memory",
		Long:  "Insert a memory into the store. Supports type hints via prefix\n(lesson:, decision:, gotcha:, pattern:). Default type: self_report.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := defaultMemoryStore()
			if err != nil {
				return fmt.Errorf("remember: %w", err)
			}
			text := strings.Join(args, " ")
			memType, content := parseTypePrefix(text)

			id, insertErr := store.Insert(context.Background(), memory.InsertParams{
				Content:    content,
				Type:       memType,
				Source:     "cli",
				Confidence: 0.8,
			})
			if insertErr != nil {
				return fmt.Errorf("remember: %w", insertErr)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Remembered (id=%d, type=%s): %s\n", id, memType, content)
			return nil
		},
	}
}
