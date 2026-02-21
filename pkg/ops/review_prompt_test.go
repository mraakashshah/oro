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
	want := "opus"
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

func TestBuildMergePrompt_IncludesRebaseInstructions(t *testing.T) {
	opts := MergeOpts{
		Branch:        "agent/oro-test",
		ConflictFiles: []string{"file1.go", "file2.go"},
	}

	prompt := buildMergePrompt(opts)

	// Check for rebase instructions
	if !strings.Contains(prompt, "git rebase main") {
		t.Error("prompt missing 'git rebase main' instruction")
	}
	if !strings.Contains(prompt, "agent/oro-test") {
		t.Error("prompt missing branch name")
	}
	if !strings.Contains(prompt, "git add") {
		t.Error("prompt missing 'git add' instruction")
	}
	if !strings.Contains(prompt, "git rebase --continue") {
		t.Error("prompt missing 'git rebase --continue' instruction")
	}
}

// --- Mutation-killer tests ---

// TestWriteHeader_ContentAndTargetBranch verifies the exact header text includes
// the target branch, the "reviewing code changes before merge" phrase, and the
// TaskOutput prohibition. A mutant that changes the phrase or removes the branch
// substitution will be caught here.
func TestWriteHeader_ContentAndTargetBranch(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-hdr",
		Worktree:    tmpDir,
		BaseBranch:  "staging",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	checks := []struct {
		substr string
		label  string
	}{
		{"You are reviewing code changes before merge to staging", "header merge phrase with branch"},
		{"MUST NOT use the TaskOutput tool", "TaskOutput prohibition"},
		{"tail -f", "tail -f danger explanation"},
		{"All commands must run in the foreground", "foreground requirement"},
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c.substr) {
			t.Errorf("prompt missing %s: want substring %q", c.label, c.substr)
		}
	}
}

// TestWriteHeader_DefaultBranchInHeaderPhrase ensures "main" appears in the
// header merge phrase when no base branch is set (default path).
func TestWriteHeader_DefaultBranchInHeaderPhrase(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-hdr",
		Worktree:    tmpDir,
		ProjectRoot: tmpDir,
		// BaseBranch intentionally empty — must default to "main"
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "You are reviewing code changes before merge to main") {
		t.Error("header must say 'before merge to main' when base branch is empty")
	}
}

// TestWriteVerdictAndOutput_Keywords checks that the verdict section contains
// exactly the verdict keywords that the reviewer is expected to produce.
// Mutants that remove or rename REJECTED / APPROVED / Critical / Important are caught.
func TestWriteVerdictAndOutput_Keywords(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-verdict",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	keywords := []string{"REJECTED", "APPROVED", "Critical", "Important"}
	for _, kw := range keywords {
		if !strings.Contains(prompt, kw) {
			t.Errorf("verdict section missing keyword %q", kw)
		}
	}
}

// TestWriteVerdictAndOutput_RejectionRules verifies the specific rejection
// conditions are present so that mutants dropping individual rules are caught.
func TestWriteVerdictAndOutput_RejectionRules(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-verdict",
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	rules := []string{
		"Any Critical → REJECTED",
		"2+ Important → REJECTED",
		"Minor only → APPROVED",
	}
	for _, r := range rules {
		if !strings.Contains(prompt, r) {
			t.Errorf("verdict section missing rule: %q", r)
		}
	}
}

// TestWritePhases_ExplicitBaseBranch verifies that the git diff commands in
// Phase 1 use the explicitly provided base branch (not a hardcoded default).
// A mutant that ignores the base parameter in writePhases is caught here.
func TestWritePhases_ExplicitBaseBranch(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-phases",
		Worktree:    tmpDir,
		BaseBranch:  "release/v2",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "git diff release/v2 --stat") {
		t.Error("Phase 1 must include 'git diff release/v2 --stat'")
	}
	if !strings.Contains(prompt, "git diff release/v2 ") {
		t.Error("Phase 1 must include 'git diff release/v2' for full diff")
	}
}

// TestReadProjectStandards_SkipsDirectories verifies that subdirectories inside
// .claude/rules/ are not read as files. The mutation that removes the IsDir()
// check would cause os.ReadFile to fail silently or panic; this test ensures
// directory entries are cleanly skipped and do not appear as content.
func TestReadProjectStandards_SkipsDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	rulesDir := filepath.Join(tmpDir, ".claude", "rules")
	//nolint:gosec // test directory
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory inside rules/ — this must be skipped
	subDir := filepath.Join(rulesDir, "nested")
	//nolint:gosec // test directory
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a legitimate rule file
	//nolint:gosec // test file
	if err := os.WriteFile(filepath.Join(rulesDir, "valid.md"), []byte("valid rule content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Place a file inside the subdirectory that should NOT be read
	//nolint:gosec // test file
	if err := os.WriteFile(filepath.Join(subDir, "hidden.md"), []byte("should not appear"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := readProjectStandards(tmpDir)

	if !strings.Contains(result, "valid rule content") {
		t.Error("readProjectStandards must include valid .md rule files")
	}
	if strings.Contains(result, "should not appear") {
		t.Error("readProjectStandards must NOT read files inside subdirectories of .claude/rules/")
	}
}

// TestBeadTitle_EmptyCase ensures the ' — ' separator is absent when BeadTitle
// is empty. The mutation that always writes the separator is caught here.
func TestBeadTitle_EmptyCase(t *testing.T) {
	tmpDir := t.TempDir()
	opts := ReviewOpts{
		BeadID:      "oro-notitle",
		BeadTitle:   "", // explicitly empty
		Worktree:    tmpDir,
		BaseBranch:  "main",
		ProjectRoot: tmpDir,
	}

	prompt := buildReviewPrompt(opts)

	if !strings.Contains(prompt, "oro-notitle") {
		t.Error("bead ID must appear in prompt")
	}
	// The separator must NOT appear when title is empty
	if strings.Contains(prompt, "oro-notitle — ") {
		t.Error("separator ' — ' must not appear when BeadTitle is empty")
	}
}

// TestExtractPatterns_EmptyPatternValue ensures that a "PATTERN: " line with
// nothing after the colon does NOT produce an empty string entry. The mutation
// that removes the `if pattern != ""` guard is caught here.
func TestExtractPatterns_EmptyPatternValue(t *testing.T) {
	stdout := "APPROVED\n\nPATTERN: \nPATTERN:  \nPATTERN: real: tag → fix\n"
	patterns := ExtractPatterns(stdout)

	for _, p := range patterns {
		if p == "" {
			t.Error("ExtractPatterns must not include empty pattern strings")
		}
	}
	if len(patterns) != 1 {
		t.Errorf("expected exactly 1 non-empty pattern, got %d: %v", len(patterns), patterns)
	}
	if patterns[0] != "real: tag → fix" {
		t.Errorf("unexpected pattern value: %q", patterns[0])
	}
}

// TestReadAntiPatterns_MissingFile verifies that readAntiPatterns returns an
// empty string when .claude/review-patterns.md does not exist.
func TestReadAntiPatterns_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	// No .claude/review-patterns.md created

	result := readAntiPatterns(tmpDir)

	if result != "" {
		t.Errorf("readAntiPatterns with missing file must return empty string, got %q", result)
	}
}

// TestReadProjectStandards_MissingClaudeMd verifies that readProjectStandards
// still returns rules content even when CLAUDE.md is absent (partial setup).
func TestReadProjectStandards_MissingClaudeMd(t *testing.T) {
	tmpDir := t.TempDir()

	rulesDir := filepath.Join(tmpDir, ".claude", "rules")
	//nolint:gosec // test directory
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // test file
	if err := os.WriteFile(filepath.Join(rulesDir, "only-rule.md"), []byte("only rule here"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No CLAUDE.md

	result := readProjectStandards(tmpDir)

	if !strings.Contains(result, "only rule here") {
		t.Error("readProjectStandards must return rules content even without CLAUDE.md")
	}
}
