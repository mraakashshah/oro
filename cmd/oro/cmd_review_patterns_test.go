package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReviewPatternsRootCommandRegistered(t *testing.T) {
	t.Run("command is registered in root", func(t *testing.T) {
		root := newRootCmd()
		found := false
		for _, sub := range root.Commands() {
			if sub.Name() == "review-patterns" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("expected 'review-patterns' subcommand in root")
		}
	})

	t.Run("candidates subcommand registered under review-patterns", func(t *testing.T) {
		root := newRootCmd()
		var rpCmd *cobra.Command
		for _, sub := range root.Commands() {
			if sub.Name() == "review-patterns" {
				rpCmd = sub
				break
			}
		}
		if rpCmd == nil {
			t.Fatal("review-patterns command not found in root")
		}
		found := false
		for _, sub := range rpCmd.Commands() {
			if sub.Name() == "candidates" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("expected 'candidates' subcommand under review-patterns")
		}
	})

	t.Run("candidates subcommand missing file prints path and no candidates", func(t *testing.T) {
		repoRoot := t.TempDir()
		oroDir := filepath.Join(repoRoot, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: testproject\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(repoRoot)

		var buf bytes.Buffer
		root := newRootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"review-patterns", "candidates"})

		if err := root.Execute(); err != nil {
			t.Fatalf("review-patterns candidates: %v", err)
		}

		got := buf.String()
		expectedPath := filepath.Join(repoRoot, ".oro", "review-pattern-candidates.md")
		if !strings.Contains(got, expectedPath) {
			t.Errorf("output should contain candidate path %q, got:\n%s", expectedPath, got)
		}
		if !strings.Contains(got, "no candidates") {
			t.Errorf("output should mention 'no candidates' when file is missing, got:\n%s", got)
		}
	})

	t.Run("candidates subcommand prints content when file exists", func(t *testing.T) {
		repoRoot := t.TempDir()
		oroDir := filepath.Join(repoRoot, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: testproject\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		candidatePath := filepath.Join(oroDir, "review-pattern-candidates.md")
		content := "# Candidate patterns\n\n- Always check for nil pointers\n"
		if err := os.WriteFile(candidatePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(repoRoot)

		var buf bytes.Buffer
		root := newRootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"review-patterns", "candidates"})

		if err := root.Execute(); err != nil {
			t.Fatalf("review-patterns candidates: %v", err)
		}

		got := buf.String()
		if !strings.Contains(got, content) {
			t.Errorf("output should contain file content, got:\n%s", got)
		}
	})

	t.Run("stealth mode uses candidates path under stealthDir", func(t *testing.T) {
		repoRoot := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)

		hash, err := projectHash(repoRoot)
		if err != nil {
			t.Fatalf("projectHash: %v", err)
		}
		stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)
		if err := os.MkdirAll(stealthDir, 0o750); err != nil {
			t.Fatal(err)
		}
		stealthConfig := filepath.Join(stealthDir, "config.yaml")
		if err := os.WriteFile(stealthConfig, []byte("mode: stealth\nproject: stealth-project\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(repoRoot)

		var buf bytes.Buffer
		root := newRootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"review-patterns", "candidates"})

		if err = root.Execute(); err != nil {
			t.Fatalf("review-patterns candidates (stealth): %v", err)
		}

		got := buf.String()
		expectedPath := filepath.Join(stealthDir, "review-pattern-candidates.md")
		if !strings.Contains(got, expectedPath) {
			t.Errorf("stealth mode output should contain stealth candidate path %q, got:\n%s", expectedPath, got)
		}
	})
}
