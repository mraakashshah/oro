package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandIncludesBead(t *testing.T) {
	root := newRootCmd()

	for _, cmd := range root.Commands() {
		if cmd.Name() == "bead" {
			return
		}
	}

	t.Fatal("root command did not register bead subcommand")
}

func TestBeadCommandHelpExposesSubcommands(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"bead", "--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected help error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"create",
		"update",
		"close",
		"list",
		"show",
		"ready",
		"blocked",
		"closed",
		"dep",
		"export",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bead help missing %q:\n%s", want, out)
		}
	}
}

func TestBeadDepCommandHelpExposesSubcommands(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"bead", "dep", "--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected dep help error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"add", "rm", "list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bead dep help missing %q:\n%s", want, out)
		}
	}
}
