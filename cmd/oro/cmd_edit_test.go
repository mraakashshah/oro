package main

import (
	"bytes"
	"strings"
	"testing"
)

// editSubcommands is the authoritative list of the 12 worker-facing edit subcommands.
var editSubcommands = []string{
	"replace",
	"after",
	"delete",
	"rename",
	"rename-all",
	"move",
	"move-to-file",
	"read",
	"diff",
	"undo",
	"batch",
	"check",
}

// TestEditSubcommandSurface verifies that:
//  1. All 12 subcommands are registered under 'oro edit'.
//  2. 'oro edit --help' lists all 12 subcommand names.
//  3. Each subcommand's --help returns exit 0 with non-empty usage.
func TestEditSubcommandSurface(t *testing.T) {
	root := newRootCmd()

	// Verify 'oro edit --help' lists all 12 subcommands.
	t.Run("edit_help_lists_all", func(t *testing.T) {
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"edit", "--help"})

		if err := root.Execute(); err != nil {
			t.Fatalf("oro edit --help: unexpected error: %v", err)
		}

		out := buf.String()
		if out == "" {
			t.Fatal("oro edit --help: empty output")
		}

		for _, sub := range editSubcommands {
			if !strings.Contains(out, sub) {
				t.Errorf("oro edit --help: expected subcommand %q in output, got:\n%s", sub, out)
			}
		}
	})

	// Find the 'edit' command in root.
	editCmd, _, err := root.Find([]string{"edit"})
	if err != nil || editCmd == nil || editCmd == root {
		t.Fatalf("'edit' command not found in root: %v", err)
	}

	// Build a map of registered subcommand names.
	registered := make(map[string]bool)
	for _, sub := range editCmd.Commands() {
		registered[sub.Name()] = true
	}

	// Verify all 12 are registered.
	t.Run("all_12_registered", func(t *testing.T) {
		for _, sub := range editSubcommands {
			if !registered[sub] {
				t.Errorf("subcommand %q not registered under 'oro edit'", sub)
			}
		}
	})

	// Verify each subcommand --help returns 0 with non-empty usage.
	t.Run("each_subcommand_help_nonempty", func(t *testing.T) {
		for _, sub := range editSubcommands {
			sub := sub
			t.Run(sub, func(t *testing.T) {
				r := newRootCmd()
				var buf bytes.Buffer
				r.SetOut(&buf)
				r.SetErr(&buf)
				r.SetArgs([]string{"edit", sub, "--help"})

				if err := r.Execute(); err != nil {
					t.Fatalf("oro edit %s --help: unexpected error: %v", sub, err)
				}

				out := buf.String()
				if out == "" {
					t.Errorf("oro edit %s --help: empty usage output", sub)
				}
			})
		}
	})
}
