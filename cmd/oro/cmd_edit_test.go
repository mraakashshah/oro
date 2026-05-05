package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestEditSubcommandSurface verifies that all 12 edit subcommands are registered
// under 'oro edit', each returns exit 0 with non-empty usage on --help, and the
// top-level 'oro edit --help' lists all 12 subcommands.
func TestEditSubcommandSurface(t *testing.T) {
	t.Parallel()

	wantSubcmds := []string{
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

	// 1. Verify 'oro edit' command is registered.
	root := newRootCmd()
	editCmd, _, err := root.Find([]string{"edit"})
	if err != nil || editCmd == nil || editCmd.Name() != "edit" {
		t.Fatal("expected 'oro edit' command to be registered under root")
	}

	// 2. Verify all 12 subcommands are registered.
	for _, name := range wantSubcmds {
		sub, _, findErr := root.Find([]string{"edit", name})
		if findErr != nil || sub == nil || sub.Name() != name {
			t.Errorf("expected 'oro edit %s' to be a registered subcommand", name)
		}
	}

	// 3. Each subcommand --help returns exit 0 with non-empty usage output.
	for _, name := range wantSubcmds {
		name := name
		t.Run(name+"_help_noerr", func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			r := newRootCmd()
			r.SetOut(&buf)
			r.SetErr(&buf)
			r.SetArgs([]string{"edit", name, "--help"})
			if execErr := r.Execute(); execErr != nil {
				t.Errorf("oro edit %s --help returned error: %v", name, execErr)
			}
			if buf.Len() == 0 {
				t.Errorf("oro edit %s --help returned empty output", name)
			}
		})
	}

	// 4. 'oro edit --help' lists all 12 subcommand names.
	var helpBuf bytes.Buffer
	r2 := newRootCmd()
	r2.SetOut(&helpBuf)
	r2.SetErr(&helpBuf)
	r2.SetArgs([]string{"edit", "--help"})
	if execErr := r2.Execute(); execErr != nil {
		t.Fatalf("oro edit --help returned error: %v", execErr)
	}
	helpOut := helpBuf.String()
	for _, name := range wantSubcmds {
		if !strings.Contains(helpOut, name) {
			t.Errorf("'oro edit --help' output does not list subcommand %q; got:\n%s", name, helpOut)
		}
	}
}
