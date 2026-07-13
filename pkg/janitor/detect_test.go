package janitor_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/janitor"
)

func TestJanitorDetectScript(t *testing.T) {
	t.Parallel()

	worktree := t.TempDir()
	scriptDir := filepath.Join(worktree, "scripts")
	if err := os.Mkdir(scriptDir, 0o750); err != nil {
		t.Fatalf("create script directory: %v", err)
	}
	script := `#!/usr/bin/env bash
printf '%s\n' '{"detector":"deadcode","file":"pkg/example.go","line":14,"title":"unused helper","detail":"remove helper"}'
printf '%s\n' 'not valid json'
printf '%s\n' '{"detector":"todo","file":"README.md","line":3,"title":"stale todo","detail":"resolve item"}'
`
	if err := os.WriteFile(filepath.Join(scriptDir, "janitor_detect.sh"), []byte(script), 0o750); err != nil {
		t.Fatalf("write detector script: %v", err)
	}

	cands, skippedLines, found, err := janitor.RunDetectScript(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run detector script: %v", err)
	}
	if !found {
		t.Fatal("expected detector script to be found")
	}
	wantCands := []janitor.Candidate{
		{Detector: "deadcode", File: "pkg/example.go", Line: 14, Title: "unused helper", Detail: "remove helper"},
		{Detector: "todo", File: "README.md", Line: 3, Title: "stale todo", Detail: "resolve item"},
	}
	if !reflect.DeepEqual(cands, wantCands) {
		t.Errorf("candidates = %#v, want %#v", cands, wantCands)
	}
	if !reflect.DeepEqual(skippedLines, []string{"not valid json"}) {
		t.Errorf("skipped lines = %#v, want %#v", skippedLines, []string{"not valid json"})
	}
}

func TestRunDetectScriptMissing(t *testing.T) {
	t.Parallel()

	cands, skippedLines, found, err := janitor.RunDetectScript(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("missing script error = %v, want nil", err)
	}
	if found {
		t.Fatal("found = true, want false")
	}
	if cands != nil {
		t.Errorf("candidates = %#v, want nil", cands)
	}
	if skippedLines != nil {
		t.Errorf("skipped lines = %#v, want nil", skippedLines)
	}
}

func TestRunDetectScriptExitFailureIncludesOutput(t *testing.T) {
	t.Parallel()

	worktree := t.TempDir()
	scriptDir := filepath.Join(worktree, "scripts")
	if err := os.Mkdir(scriptDir, 0o750); err != nil {
		t.Fatalf("create script directory: %v", err)
	}
	script := "#!/usr/bin/env bash\necho detector failed >&2\nexit 7\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "janitor_detect.sh"), []byte(script), 0o750); err != nil {
		t.Fatalf("write detector script: %v", err)
	}

	_, _, found, err := janitor.RunDetectScript(context.Background(), worktree)
	if !found {
		t.Fatal("expected detector script to be found")
	}
	if err == nil {
		t.Fatal("expected non-zero detector exit to return an error")
	}
	if !strings.Contains(err.Error(), "detector failed") {
		t.Errorf("error = %q, want detector output", err)
	}
}

func TestJanitorBuiltinsSkipMissing(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "pyproject.toml"), []byte("[project]\nname = 'fixture'\n"), 0o600); err != nil {
		t.Fatalf("write Python project marker: %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if contains(ran, "vulture") {
		t.Errorf("ran = %#v, should not include missing vulture", ran)
	}
	if !contains(skipped, "vulture") {
		t.Errorf("skipped = %#v, want vulture", skipped)
	}
	for _, candidate := range cands {
		if candidate.Detector == "vulture" {
			t.Errorf("candidates = %#v, want no vulture findings", cands)
		}
		if candidate.Detector == "" {
			t.Errorf("candidate = %#v, want detector name", candidate)
		}
	}
}

func TestCIDetectorEmitsFindingWhenRed(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	binDir := t.TempDir()
	gh := `#!/bin/sh
[ "$1" = run ] && [ "$2" = list ] && [ "$3" = --branch ] && [ "$4" = main ] || exit 1
printf '%s\n' '[{"workflowName":"CI","displayTitle":"unit tests","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/42"},{"workflowName":"Release","displayTitle":"publish","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/43"}]'
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "ci") {
		t.Errorf("ran = %#v, want ci", ran)
	}
	if contains(skipped, "ci") {
		t.Errorf("skipped = %#v, should not include ci", skipped)
	}
	want := janitor.Candidate{
		Detector: "ci",
		Title:    "CI failed",
		Detail:   "workflow: CI; job: unit tests; run: https://github.example/acme/repo/actions/runs/42",
	}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
	want = janitor.Candidate{
		Detector: "ci",
		Title:    "Release failed",
		Detail:   "workflow: Release; job: publish; run: https://github.example/acme/repo/actions/runs/43",
	}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
}

func TestCIDetectorNoopWhenToolMissing(t *testing.T) {
	worktree := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if contains(ran, "ci") {
		t.Errorf("ran = %#v, should not include ci", ran)
	}
	if !contains(skipped, "ci") {
		t.Errorf("skipped = %#v, want ci", skipped)
	}
	for _, candidate := range cands {
		if candidate.Detector == "ci" {
			t.Errorf("candidates = %#v, want no ci finding", cands)
		}
	}
}

func TestCIDetectorNoopWhenUnauthed(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	binDir := t.TempDir()
	gh := "#!/bin/sh\necho 'authentication required' >&2\nexit 4\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if contains(ran, "ci") {
		t.Errorf("ran = %#v, should not include ci", ran)
	}
	if !contains(skipped, "ci") {
		t.Errorf("skipped = %#v, want ci", skipped)
	}
	for _, candidate := range cands {
		if candidate.Detector == "ci" {
			t.Errorf("candidates = %#v, want no ci finding", cands)
		}
	}
}

func TestCIDetectorNoopWhenGreen(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	binDir := t.TempDir()
	gh := "#!/bin/sh\nprintf '%s\\n' '[{\"workflowName\":\"CI\",\"conclusion\":\"success\",\"url\":\"https://github.example/acme/repo/actions/runs/42\"}]'\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "ci") || contains(skipped, "ci") {
		t.Errorf("ran, skipped = %#v, %#v, want ci ran only", ran, skipped)
	}
	for _, candidate := range cands {
		if candidate.Detector == "ci" {
			t.Errorf("candidates = %#v, want no ci finding", cands)
		}
	}
}

func TestCIDetectorNoopOutsideGitWorktree(t *testing.T) {
	binDir := t.TempDir()
	gh := "#!/bin/sh\nprintf '%s\\n' '[]'\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if contains(ran, "ci") {
		t.Errorf("ran = %#v, should not include ci", ran)
	}
	if !contains(skipped, "ci") {
		t.Errorf("skipped = %#v, want ci", skipped)
	}
	if containsCandidate(cands, janitor.Candidate{Detector: "ci"}) {
		t.Errorf("candidates = %#v, want no ci finding", cands)
	}
}

func TestJanitorBuiltinsKeepsLintFindings(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "pyproject.toml"), []byte("[project]\nname = 'fixture'\n"), 0o600); err != nil {
		t.Fatalf("write Python project marker: %v", err)
	}
	binDir := t.TempDir()
	vulturePath := filepath.Join(binDir, "vulture")
	vulture := "#!/bin/sh\nprintf '%s\\n' 'unused helper'\nexit 1\n"
	if err := os.WriteFile(vulturePath, []byte(vulture), 0o700); err != nil {
		t.Fatalf("write vulture fixture: %v", err)
	}
	t.Setenv("PATH", binDir)

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "vulture") {
		t.Errorf("ran = %#v, want vulture", ran)
	}
	if contains(skipped, "vulture") {
		t.Errorf("skipped = %#v, should not include vulture", skipped)
	}
	if !reflect.DeepEqual(cands, []janitor.Candidate{{Detector: "vulture", Title: "unused helper", Detail: "unused helper"}}) {
		t.Errorf("candidates = %#v, want vulture finding", cands)
	}
}

func TestJanitorBuiltinsTreatsDetectorStderrAsCrash(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "pyproject.toml"), []byte("[project]\nname = 'fixture'\n"), 0o600); err != nil {
		t.Fatalf("write Python project marker: %v", err)
	}
	binDir := t.TempDir()
	vulturePath := filepath.Join(binDir, "vulture")
	vulture := "#!/bin/sh\nprintf '%s\\n' 'configuration crashed' >&2\nexit 1\n"
	if err := os.WriteFile(vulturePath, []byte(vulture), 0o700); err != nil {
		t.Fatalf("write vulture fixture: %v", err)
	}
	t.Setenv("PATH", binDir)

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree)
	if err == nil {
		t.Fatal("run built-in detectors succeeded, want detector crash error")
	}
	if !strings.Contains(err.Error(), "configuration crashed") {
		t.Errorf("error = %q, want detector stderr", err)
	}
	if contains(ran, "vulture") {
		t.Errorf("ran = %#v, should not include crashed vulture", ran)
	}
	for _, candidate := range cands {
		if candidate.Detector == "vulture" {
			t.Errorf("candidates = %#v, want no vulture crash output", cands)
		}
	}
}

func TestJanitorBuiltinsFindsOrphanFiles(t *testing.T) {
	worktree := t.TempDir()
	assetsDir := filepath.Join(worktree, "assets")
	if err := os.Mkdir(assetsDir, 0o750); err != nil {
		t.Fatalf("create assets directory: %v", err)
	}
	for name, contents := range map[string]string{
		"used.svg":   "used",
		"unused.svg": "unused",
	} {
		if err := os.WriteFile(filepath.Join(assetsDir, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write asset %q: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("![used](assets/used.svg)\n"), 0o600); err != nil {
		t.Fatalf("write asset reference: %v", err)
	}

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "orphan-files") {
		t.Errorf("ran = %#v, want orphan-files", ran)
	}
	want := janitor.Candidate{Detector: "orphan-files", File: "assets/unused.svg", Title: "orphan file", Detail: "unreferenced asset or script"}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
	for _, candidate := range cands {
		if candidate.Detector == "orphan-files" && candidate.File == "assets/used.svg" {
			t.Errorf("candidates = %#v, referenced asset should not be orphaned", cands)
		}
	}
}

func TestJanitorBuiltinsUsesGitHistoryForTODOAge(t *testing.T) {
	worktree := t.TempDir()
	oldPath := filepath.Join(worktree, "old.go")
	if err := os.WriteFile(oldPath, []byte("package fixture // TODO remove legacy path\n"), 0o600); err != nil {
		t.Fatalf("write TODO fixture: %v", err)
	}
	runGit(t, worktree, "init")
	runGit(t, worktree, "config", "user.email", "janitor@example.com")
	runGit(t, worktree, "config", "user.name", "Janitor Test")
	runGit(t, worktree, "add", "old.go")
	oldDate := time.Now().AddDate(0, 0, -61).Format(time.RFC3339)
	runGitWithEnv(t, worktree, []string{"GIT_AUTHOR_DATE=" + oldDate, "GIT_COMMITTER_DATE=" + oldDate}, "commit", "-m", "add old TODO")
	now := time.Now()
	if err := os.Chtimes(oldPath, now, now); err != nil {
		t.Fatalf("refresh TODO file mtime: %v", err)
	}

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "todo") {
		t.Errorf("ran = %#v, want todo", ran)
	}
	want := janitor.Candidate{Detector: "todo", File: "old.go", Line: 1, Title: "stale TODO/FIXME", Detail: "package fixture // TODO remove legacy path"}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
}

func TestJanitorBuiltinsFindsBrokenRelativeLinksWithoutTools(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("[missing](docs/missing.md)\n"), 0o600); err != nil {
		t.Fatalf("write Markdown fixture: %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree)
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "broken-links") {
		t.Errorf("ran = %#v, want broken-links", ran)
	}
	want := janitor.Candidate{Detector: "broken-links", File: "README.md", Line: 1, Title: "broken relative link", Detail: "docs/missing.md"}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsCandidate(candidates []janitor.Candidate, target janitor.Candidate) bool {
	for _, candidate := range candidates {
		if candidate == target {
			return true
		}
	}
	return false
}

func runGit(t *testing.T, worktree string, args ...string) {
	t.Helper()
	runGitWithEnv(t, worktree, nil, args...)
}

func runGitWithEnv(t *testing.T, worktree string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func TestCandidateShape(t *testing.T) {
	t.Parallel()

	candidateType := reflect.TypeFor[janitor.Candidate]()
	wantNames := []string{"Detector", "File", "Title", "Detail", "Line"}
	for i, wantName := range wantNames {
		field := candidateType.Field(i)
		if field.Name != wantName {
			t.Errorf("field %d = %q, want %q", i, field.Name, wantName)
		}
	}
}
