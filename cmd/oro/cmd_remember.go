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

// newRememberCmdWithStore creates the "oro remember" subcommand.
// If store is nil, the command lazily opens the default store on execution.
func newRememberCmdWithStore(store *memory.Store) *cobra.Command {
	var pin bool
	cmd := &cobra.Command{
		Use:   "remember <text>",
		Short: "Store a memory",
		Long:  "Insert a memory into the store. Supports type hints via prefix\n(lesson:, decision:, gotcha:, pattern:). Default type: self_report.\nUse --pin to mark memory as permanent (no time decay).",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Lazy store initialization if not provided
			s := store
			if s == nil {
				var err error
				s, err = defaultMemoryStore()
				if err != nil {
					return fmt.Errorf("remember: %w", err)
				}
			}

			text := strings.Join(args, " ")
			memType, content := parseTypePrefix(text)

			id, err := s.Insert(context.Background(), memory.InsertParams{
				Content:    content,
				Type:       memType,
				Source:     "cli",
				Confidence: 0.8,
				Pinned:     pin,
			})
			if err != nil {
				return fmt.Errorf("remember: %w", err)
			}

			pinnedTag := ""
			if pin {
				pinnedTag = " [pinned]"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Remembered (id=%d, type=%s)%s: %s\n", id, memType, pinnedTag, content)
			return nil
		},
	}
	cmd.Flags().BoolVar(&pin, "pin", false, "Pin memory (skip time decay)")
	return cmd
}
