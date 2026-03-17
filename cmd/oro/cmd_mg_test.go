package main

import (
	"testing"
)

func TestMgFlagParsing(t *testing.T) {
	cmd := newMgCmd()

	// Verify the command name and description.
	if cmd.Use != "mg" {
		t.Fatalf("expected Use='mg', got %q", cmd.Use)
	}

	// Check all required flags exist.
	flags := []struct {
		name     string
		defValue string
	}{
		{"path", ""},
		{"block-types", ""},
		{"status", "false"},
	}
	for _, f := range flags {
		flag := cmd.Flag(f.name)
		if flag == nil {
			t.Fatalf("expected --%s flag to exist", f.name)
		}
		if flag.DefValue != f.defValue {
			t.Fatalf("--%s default: expected %q, got %q", f.name, f.defValue, flag.DefValue)
		}
	}
}

func TestMgCmd_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "mg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'mg' command to be registered in root")
	}
}
