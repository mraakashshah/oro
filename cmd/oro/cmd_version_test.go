package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

func TestVersionSubcommandParity(t *testing.T) {
	root := newRootCmd()
	want := root.Version + "\n"

	versionOutput := executeRootCommand(t, root, "version")
	if versionOutput != want {
		t.Fatalf("oro version output = %q, want %q", versionOutput, want)
	}

	flagOutput := executeRootCommand(t, newRootCmd(), "--version")
	if versionOutput != flagOutput {
		t.Fatalf("oro version output = %q, oro --version output = %q", versionOutput, flagOutput)
	}
}

func executeRootCommand(t *testing.T, root *cobra.Command, args ...string) string {
	t.Helper()
	var stdout bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&stdout)
	if err := root.Execute(); err != nil {
		t.Fatalf("oro %v: %v", args, err)
	}
	return stdout.String()
}
