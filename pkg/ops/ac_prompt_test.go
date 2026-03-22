package ops //nolint:testpackage // internal test needs access to unexported buildACPrompt

import (
	"strings"
	"testing"
)

func TestBuildACPrompt(t *testing.T) {
	opts := WriteACOpts{
		BeadID:          "oro-test1",
		BeadTitle:       "Implement foo service",
		BeadDescription: "Add a foo service that does bar.",
		Workdir:         "/some/project",
	}

	got := buildACPrompt(opts)

	t.Run("bd show present", func(t *testing.T) {
		if !strings.Contains(got, "bd show") {
			t.Errorf("prompt missing 'bd show'; got:\n%s", got)
		}
	})

	t.Run("--acceptance present", func(t *testing.T) {
		if !strings.Contains(got, "--acceptance") {
			t.Errorf("prompt missing '--acceptance'; got:\n%s", got)
		}
	})

	t.Run("format spec present", func(t *testing.T) {
		if !strings.Contains(got, "Test:") {
			t.Errorf("prompt missing 'Test:'; got:\n%s", got)
		}
		if !strings.Contains(got, "Cmd:") {
			t.Errorf("prompt missing 'Cmd:'; got:\n%s", got)
		}
		if !strings.Contains(got, "Assert:") {
			t.Errorf("prompt missing 'Assert:'; got:\n%s", got)
		}
	})

	t.Run("TaskOutput prohibition present", func(t *testing.T) {
		if !strings.Contains(got, "TaskOutput") {
			t.Errorf("prompt missing 'TaskOutput' prohibition; got:\n%s", got)
		}
	})

	t.Run("codebase exploration instruction present", func(t *testing.T) {
		// Must instruct agent to grep/glob codebase for symbols/files
		hasGrep := strings.Contains(got, "Grep") || strings.Contains(got, "grep") ||
			strings.Contains(got, "Glob") || strings.Contains(got, "glob")
		if !hasGrep {
			t.Errorf("prompt missing codebase exploration instruction (Grep/Glob); got:\n%s", got)
		}
	})

	t.Run("bead id injected", func(t *testing.T) {
		if !strings.Contains(got, opts.BeadID) {
			t.Errorf("prompt missing bead ID %q; got:\n%s", opts.BeadID, got)
		}
	})

	t.Run("bead title injected", func(t *testing.T) {
		if !strings.Contains(got, opts.BeadTitle) {
			t.Errorf("prompt missing bead title %q; got:\n%s", opts.BeadTitle, got)
		}
	})

	t.Run("no worktree creation instruction", func(t *testing.T) {
		lower := strings.ToLower(got)
		// Prohibitions like "Do NOT create worktrees" are fine.
		// Only affirmative instructions to create a worktree are forbidden.
		if strings.Contains(lower, "git worktree add") || strings.Contains(lower, "create a worktree") {
			t.Errorf("prompt must NOT affirmatively instruct agent to create worktrees; got:\n%s", got)
		}
	})

	t.Run("no write source code instruction", func(t *testing.T) {
		lower := strings.ToLower(got)
		// Prohibitions like "Do NOT write source code" are fine.
		// Only affirmative instructions to implement code are forbidden.
		if strings.Contains(lower, "implement the feature") || strings.Contains(lower, "implement the code") {
			t.Errorf("prompt must NOT affirmatively instruct agent to write source code; got:\n%s", got)
		}
	})

	t.Run("bd update with acceptance instruction present", func(t *testing.T) {
		if !strings.Contains(got, "bd update") {
			t.Errorf("prompt missing 'bd update' instruction; got:\n%s", got)
		}
	})

	t.Run("docs/plans exploration instruction present", func(t *testing.T) {
		if !strings.Contains(got, "docs/plans") {
			t.Errorf("prompt missing docs/plans exploration; got:\n%s", got)
		}
	})

	t.Run("deps exploration instruction present", func(t *testing.T) {
		// Must instruct agent to check blocking/blocked beads
		hasDeps := strings.Contains(got, "blocking") || strings.Contains(got, "blocked") ||
			strings.Contains(got, "depend")
		if !hasDeps {
			t.Errorf("prompt missing dependency exploration instruction; got:\n%s", got)
		}
	})
}

func TestBuildACPromptUsesOroDocsDir(t *testing.T) {
	opts := WriteACOpts{
		BeadID:     "oro-docs-test",
		BeadTitle:  "Test bead",
		OroDocsDir: "/custom/oro/docs",
	}
	got := buildACPrompt(opts)
	if !strings.Contains(got, "/custom/oro/docs/plans") {
		t.Error("prompt must use OroDocsDir when set")
	}
	if strings.Contains(got, "`docs/plans/`") {
		t.Error("prompt must not use hardcoded docs/plans when OroDocsDir is set")
	}
}
