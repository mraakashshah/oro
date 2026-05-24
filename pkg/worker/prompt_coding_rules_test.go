package worker_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"oro/pkg/worker"
)

func TestPromptCodingRules(t *testing.T) {
	t.Parallel()

	repoRoot := workerRepoRoot(t)
	doctrinePath := filepath.Join(repoRoot, "assets", "doctrine.md")
	doctrineBytes, err := os.ReadFile(doctrinePath) //nolint:gosec // test reads repository fixture
	if err != nil {
		t.Fatalf("read doctrine asset: %v", err)
	}
	doctrine := string(doctrineBytes)

	for _, want := range []string{
		"LEVEL 1 - Lint",
		"LEVEL 2 - Types",
		"LEVEL 3 - Formatter",
		"LEVEL 4 - Pre-commit",
		"LEVEL 5 - CI",
		"LEVEL 6 - Prompt (BEST EFFORT)",
		"Example:",
		"Implementation:",
	} {
		if !strings.Contains(doctrine, want) {
			t.Fatalf("assets/doctrine.md missing %q:\n%s", want, doctrine)
		}
	}

	projectRoot := t.TempDir()
	writePromptRulesConfig(t, projectRoot, `languages:
  go:
    coding_rules:
      - "- Go: wrap errors with %w when preserving cause"
  python:
    coding_rules:
      - "- Python: prefer pytest fixtures over test classes"
`)

	prompt := worker.AssemblePrompt(worker.PromptParams{
		BeadID:             "oro-rwnuc",
		Title:              "Doctrine coding rules",
		Description:        "Render coding rules doctrine in worker prompts",
		AcceptanceCriteria: "doctrine plus per-language rules",
		WorktreePath:       "/tmp/oro-rwnuc",
		ProjectRoot:        projectRoot,
	})
	codingRules := extractSection(t, prompt, "## Coding Rules")

	if !strings.Contains(codingRules, strings.TrimSpace(doctrine)+"\n\nProject rules:") {
		t.Fatalf("Coding Rules section should consume published doctrine before project rules:\n%s", codingRules)
	}

	for _, want := range []string{
		"Enforcement Doctrine",
		"LEVEL 1 - Lint",
		"LEVEL 6 - Prompt (BEST EFFORT)",
		"- Go: wrap errors with %w when preserving cause",
		"- Python: prefer pytest fixtures over test classes",
	} {
		if !strings.Contains(codingRules, want) {
			t.Errorf("Coding Rules section missing %q:\n%s", want, codingRules)
		}
	}

	workerProgramIdx := strings.Index(prompt, "## Worker Program")
	tddIdx := strings.Index(prompt, "## TDD")
	if workerProgramIdx != -1 && workerProgramIdx < strings.Index(prompt, "## Coding Rules") {
		t.Fatalf("Worker Program rendered before Coding Rules:\n%s", prompt)
	}
	if tddIdx == -1 {
		t.Fatal("TDD section not found")
	}
	if tddIdx < strings.Index(prompt, "## Coding Rules") {
		t.Fatalf("TDD rendered before Coding Rules:\n%s", prompt)
	}
}

func workerRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func writePromptRulesConfig(t *testing.T, dir, content string) {
	t.Helper()
	oroDir := filepath.Join(dir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatalf("create .oro dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
