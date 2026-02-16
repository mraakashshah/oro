package ops //nolint:testpackage // internal test needs access to unexported types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildReviewPrompt_IncludesAcceptanceCriteria(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:             "oro-test",
		BeadTitle:          "Add widget support",
		Worktree:           tmpDir,
		AcceptanceCriteria: "Must have unit tests for all public functions",
		BaseBranch:         "main",
		ProjectRoot:        tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "oro-test") {
		t.Error("prompt missing bead ID")
	}
	if !strings.Contains(prompt, "Add widget support") {
		t.Error("prompt missing bead title")
	}
	if !strings.Contains(prompt, "Must have unit tests for all public functions") {
		t.Error("prompt missing acceptance criteria")
	}
}

func TestBuildReviewPrompt_IncludesProjectStandards(t *testing.T) {
	tmpDir := t.TempDir()

	// Create CLAUDE.md
	//nolint:gosec // Test file permissions
	err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte("# Project Rules\nUse functional patterns.\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "Use functional patterns.") {
		t.Error("prompt missing CLAUDE.md content")
	}
}

func TestBuildReviewPrompt_IncludesRules(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .claude/rules/ with a rule file
	rulesDir := filepath.Join(tmpDir, ".claude", "rules")
	//nolint:gosec // Test directory permissions
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Test file permissions
	if err := os.WriteFile(filepath.Join(rulesDir, "standards.md"), []byte("Always use early returns.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "Always use early returns.") {
		t.Error("prompt missing rules content")
	}
}

func TestBuildReviewPrompt_IncludesAntiPatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .claude/review-patterns.md
	claudeDir := filepath.Join(tmpDir, ".claude")
	//nolint:gosec // Test directory permissions
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Test file permissions
	if err := os.WriteFile(filepath.Join(claudeDir, "review-patterns.md"),
		[]byte("loose-match: strings.Contains for exact lookup\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "loose-match: strings.Contains for exact lookup") {
		t.Error("prompt missing anti-patterns content")
	}
}

func TestBuildReviewPrompt_MissingFilesGraceful(t *testing.T) {
	tmpDir := t.TempDir()

	// No CLAUDE.md, no .claude/rules, no review-patterns.md
	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	// Should not panic
	prompt := buildReviewPrompt(opts)

	// Should still have the core review instructions
	if !strings.Contains(prompt, "Phase 1: Understand") {
		t.Error("prompt missing Phase 1 instructions")
	}
	if !strings.Contains(prompt, "Phase 2: Critique") {
		t.Error("prompt missing Phase 2 instructions")
	}
}

func TestBuildReviewPrompt_BaseBranch(t *testing.T) {
	tmpDir := t.TempDir()

	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		BaseBranch:  "develop",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "git diff develop") {
		t.Error("prompt should reference the base branch 'develop'")
	}
	if strings.Contains(prompt, "git diff main") {
		t.Error("prompt should not hardcode 'main' when base branch is 'develop'")
	}
}

func TestBuildReviewPrompt_DefaultBaseBranch(t *testing.T) {
	tmpDir := t.TempDir()

	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		ProjectRoot: tmpDir,
		// BaseBranch intentionally empty
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "git diff main") {
		t.Error("prompt should default to 'main' when base branch is empty")
	}
}

func TestParseReviewOutputExtractsPatterns(t *testing.T) {
	stdout := `Looking at the code...

APPROVED

All criteria met.

PATTERN: loose-match: strings.Contains for exact lookup → use map key
PATTERN: happy-only: test covers happy path only → add error cases
`

	verdict, feedback := parseReviewOutput(stdout)
	if verdict != VerdictApproved {
		t.Fatalf("expected VerdictApproved, got %q", verdict)
	}
	_ = feedback

	patterns := ExtractPatterns(stdout)
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d: %v", len(patterns), patterns)
	}
	if patterns[0] != "loose-match: strings.Contains for exact lookup → use map key" {
		t.Errorf("unexpected pattern[0]: %q", patterns[0])
	}
	if patterns[1] != "happy-only: test covers happy path only → add error cases" {
		t.Errorf("unexpected pattern[1]: %q", patterns[1])
	}
}

func TestExtractPatternsEmpty(t *testing.T) {
	stdout := "APPROVED\n\nAll good.\n"
	patterns := ExtractPatterns(stdout)
	if len(patterns) != 0 {
		t.Fatalf("expected 0 patterns, got %d", len(patterns))
	}
}

func TestReviewModelIsOpus(t *testing.T) {
	got := OpsReview.Model()
	want := "claude-opus-4-6"
	if got != want {
		t.Fatalf("OpsReview.Model() = %q, want %q", got, want)
	}
}

func TestBuildReviewPrompt_ProhibitsTaskOutput(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-test",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	// Check for TaskOutput prohibition
	if !strings.Contains(prompt, "MUST NOT use the TaskOutput tool") {
		t.Error("prompt missing TaskOutput prohibition")
	}
	if !strings.Contains(prompt, "use the Read tool") {
		t.Error("prompt missing Read tool instruction")
	}
	if !strings.Contains(prompt, "foreground") {
		t.Error("prompt missing foreground instruction")
	}
}

func TestBuildMergePrompt_ProhibitsTaskOutput(t *testing.T) {
	opts := MergeOpts{
		ConflictFiles: []string{"file1.go", "file2.go"},
	}

	prompt := buildMergePrompt(opts)

	// Check for TaskOutput prohibition
	if !strings.Contains(prompt, "Do NOT use TaskOutput") {
		t.Error("prompt missing TaskOutput prohibition")
	}
	if !strings.Contains(prompt, "run tasks in the background") {
		t.Error("prompt missing background prohibition")
	}
	if !strings.Contains(prompt, "Use the Read tool") {
		t.Error("prompt missing Read tool instruction")
	}
	if !strings.Contains(prompt, "foreground") {
		t.Error("prompt missing foreground instruction")
	}
}

func TestBuildDiagnosisPrompt_ProhibitsTaskOutput(t *testing.T) {
	opts := DiagOpts{
		BeadID:  "oro-test",
		Symptom: "Tests failing",
	}

	prompt := buildDiagnosisPrompt(opts)

	// Check for TaskOutput prohibition
	if !strings.Contains(prompt, "Do NOT use TaskOutput") {
		t.Error("prompt missing TaskOutput prohibition")
	}
	if !strings.Contains(prompt, "run tasks in the background") {
		t.Error("prompt missing background prohibition")
	}
	if !strings.Contains(prompt, "Use the Read tool") {
		t.Error("prompt missing Read tool instruction")
	}
	if !strings.Contains(prompt, "foreground") {
		t.Error("prompt missing foreground instruction")
	}
}
