package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJanitorDetectCommand(t *testing.T) {
	registered, _, err := newRootCmd().Find([]string{"janitor:detect"})
	if err != nil {
		t.Fatalf("find registered janitor detector command: %v", err)
	}
	if registered.Name() != "janitor:detect" || !registered.Hidden {
		t.Fatalf("registered command = %q hidden=%t, want hidden janitor:detect", registered.Name(), registered.Hidden)
	}

	t.Run("reports remaining candidates without mutating the repository", func(t *testing.T) {
		worktree := t.TempDir()
		readmePath := filepath.Join(worktree, "README.md")
		contents := []byte("[missing](docs/missing.md)\n")
		if err := os.WriteFile(readmePath, contents, 0o600); err != nil {
			t.Fatalf("write README fixture: %v", err)
		}
		if err := os.WriteFile(filepath.Join(worktree, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0o600); err != nil {
			t.Fatalf("write module fixture: %v", err)
		}
		nested := filepath.Join(worktree, "pkg", "nested")
		if err := os.MkdirAll(nested, 0o750); err != nil {
			t.Fatalf("create nested working directory: %v", err)
		}
		t.Chdir(nested)

		cmd := newJanitorDetectCmd()
		if !cmd.Hidden {
			t.Fatal("janitor detector rerun command must be hidden from normal help")
		}
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs([]string{"--detector", "broken-links"})

		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "found 1 candidate") {
			t.Fatalf("candidate rerun error = %v, want one-candidate failure", err)
		}
		if !strings.Contains(output.String(), `"detector":"broken-links"`) {
			t.Fatalf("candidate output = %q, want broken-links JSON", output.String())
		}
		got, readErr := os.ReadFile(readmePath)
		if readErr != nil {
			t.Fatalf("read README after detector: %v", readErr)
		}
		if !bytes.Equal(got, contents) {
			t.Fatalf("detector mutated README\n got: %q\nwant: %q", got, contents)
		}
	})

	t.Run("returns success when the detector is clear", func(t *testing.T) {
		worktree := t.TempDir()
		if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("clean\n"), 0o600); err != nil {
			t.Fatalf("write README fixture: %v", err)
		}
		t.Chdir(worktree)

		cmd := newJanitorDetectCmd()
		cmd.SetArgs([]string{"--detector", "broken-links"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("clear detector rerun: %v", err)
		}
	})

	t.Run("rejects unknown and skipped detectors clearly", func(t *testing.T) {
		worktree := t.TempDir()
		t.Chdir(worktree)

		unknown := newJanitorDetectCmd()
		unknown.SetArgs([]string{"--detector", "not-a-detector"})
		if err := unknown.Execute(); err == nil || !strings.Contains(err.Error(), "unknown janitor detector") {
			t.Fatalf("unknown detector error = %v", err)
		}

		skipped := newJanitorDetectCmd()
		skipped.SetArgs([]string{"--detector", "ci"})
		if err := skipped.Execute(); err == nil || !strings.Contains(err.Error(), "skipped") {
			t.Fatalf("skipped detector error = %v", err)
		}
	})

	t.Run("accepts a target branch for the CI detector", func(t *testing.T) {
		cmd := newJanitorDetectCmd()
		if flag := cmd.Flags().Lookup("target-branch"); flag == nil {
			t.Fatal("janitor detector command missing --target-branch")
		}
		if err := cmd.ParseFlags([]string{"--detector", "ci", "--target-branch", "release/v1"}); err != nil {
			t.Fatalf("parse CI target branch: %v", err)
		}
		if got, err := cmd.Flags().GetString("target-branch"); err != nil || got != "release/v1" {
			t.Fatalf("parsed target branch = %q, %v; want release/v1", got, err)
		}
	})
}
