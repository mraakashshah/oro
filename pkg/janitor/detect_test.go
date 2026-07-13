package janitor_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
