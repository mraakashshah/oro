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

	cands, skippedLines, found, err := janitor.RunDetectScript(context.Background(), worktree, janitor.WithDirectExecutionForTest())
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

	cands, skippedLines, found, err := janitor.RunDetectScript(context.Background(), t.TempDir(), janitor.WithDirectExecutionForTest())
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

	_, _, found, err := janitor.RunDetectScript(context.Background(), worktree, janitor.WithDirectExecutionForTest())
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

func TestJanitorDetectCommandAPI(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("[missing](docs/missing.md)\n"), 0o600); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}

	cands, err := janitor.RunBuiltin(context.Background(), worktree, "", "broken-links", janitor.WithDirectExecutionForTest())
	if err != nil {
		t.Fatalf("run broken-links detector: %v", err)
	}
	want := janitor.Candidate{
		Detector: "broken-links",
		File:     "README.md",
		Line:     1,
		Title:    "broken relative link",
		Detail:   "docs/missing.md",
	}
	if !containsCandidate(cands, want) {
		t.Fatalf("candidates = %#v, want %#v", cands, want)
	}

	if _, err := janitor.RunBuiltin(context.Background(), worktree, "", "not-a-detector", janitor.WithDirectExecutionForTest()); err == nil || !strings.Contains(err.Error(), "unknown janitor detector") {
		t.Fatalf("unknown detector error = %v", err)
	}
	if _, err := janitor.RunBuiltin(context.Background(), worktree, "", "ci", janitor.WithDirectExecutionForTest()); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("skipped CI detector error = %v", err)
	}
}

func TestJanitorBuiltinsSkipMissing(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "pyproject.toml"), []byte("[project]\nname = 'fixture'\n"), 0o600); err != nil {
		t.Fatalf("write Python project marker: %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "", janitor.WithDirectExecutionForTest())
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

func TestFallbackCandidatesCarryEvidence(t *testing.T) {
	worktree := t.TempDir()
	for name, contents := range map[string]string{
		"go.mod":             "module fixture\n\ngo 1.26\n",
		"pyproject.toml":     "[project]\nname = 'fixture'\n",
		"pkg/dead.go":        "package fixture\nfunc unused() {}\n",
		"pkg/duplicate.go":   "package fixture\nfunc duplicate() {}\n",
		"pkg/lint.go":        "package fixture\nvar lint = true\n",
		"python/lint.py":     "import os\nprint('lint')\n",
		"python/unused.py":   "def live(): pass\ndef unused(): pass\n",
		"ci/failure_test.go": "package ci\nfunc broken() {}\n",
	} {
		writeFallbackFixture(t, worktree, name, contents, 0o600)
	}
	runGit(t, worktree, "init", "-b", "agent/janitor-scan")
	runGit(t, worktree, "remote", "add", "origin", "git@github.example:acme/repo.git")

	binDir := t.TempDir()
	tools := map[string]string{
		"deadcode":      "#!/bin/sh\nprintf '%s\\n' 'pkg/dead.go:2:6: func unused is unused'\n",
		"dupl":          "#!/bin/sh\nprintf '%s\\n' 'pkg/duplicate.go:2,2'\n",
		"golangci-lint": "#!/bin/sh\nprintf '%s\\n' 'pkg/lint.go:2:5: lint issue (revive)' 'level=warning msg=noise'\n",
		"ruff":          "#!/bin/sh\nprintf '%s\\n' 'python/lint.py:2:1: F401 imported but unused' 'Found 1 error.'\n",
		"vulture":       "#!/bin/sh\nprintf '%s\\n' \"python/unused.py:2: unused function 'unused' (60% confidence)\"\n",
		"gh": `#!/bin/sh
if [ "$1" = auth ] && [ "$2" = status ]; then exit 0; fi
if [ "$1" = run ] && [ "$2" = list ]; then
  printf '%s\n' '[{"databaseId":42,"workflowDatabaseId":100,"workflowName":"CI","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/42"}]'
  exit 0
fi
if [ "$1" = run ] && [ "$2" = view ] && [ "$4" = --json ]; then
  printf '%s\n' '{"jobs":[{"name":"unit tests","conclusion":"failure"}]}'
  exit 0
fi
if [ "$1" = run ] && [ "$2" = view ] && [ "$4" = --log-failed ]; then
  printf '%s\n' 'unit tests build ci/failure_test.go:2:6: compile failure' 'unit tests finished with status 1'
  exit 0
fi
exit 1
`,
	}
	for name, script := range tools {
		writeFallbackFixture(t, binDir, name, script, 0o700)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	candidates, _, _, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
	if err != nil {
		t.Fatalf("run fallback detectors: %v", err)
	}
	want := map[string]struct {
		file string
		line int
	}{
		"deadcode":      {file: "pkg/dead.go", line: 2},
		"dupl":          {file: "pkg/duplicate.go", line: 2},
		"golangci-lint": {file: "pkg/lint.go", line: 2},
		"ruff":          {file: "python/lint.py", line: 2},
		"vulture":       {file: "python/unused.py", line: 2},
		"ci":            {file: "ci/failure_test.go", line: 2},
	}
	for detector, location := range want {
		if !hasFallbackCandidateLocation(candidates, detector, location.file, location.line) {
			t.Errorf("%s candidates = %#v, want %s:%d", detector, candidates, location.file, location.line)
		}
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate.Detail, "msg=noise") || strings.Contains(candidate.Detail, "Found 1 error") ||
			strings.Contains(candidate.Detail, "finished with status") {
			t.Errorf("unfileable detector noise emitted as candidate: %#v", candidate)
		}
	}
}

func writeFallbackFixture(t *testing.T, root, name, contents string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write fixture %q: %v", name, err)
	}
}

func hasFallbackCandidateLocation(candidates []janitor.Candidate, detector, file string, line int) bool {
	for _, candidate := range candidates {
		if candidate.Detector == detector && candidate.File == file && candidate.Line == line {
			return true
		}
	}
	return false
}

func TestCIDetectorEmitsFindingWhenRed(t *testing.T) {
	worktree := t.TempDir()
	writeFallbackFixture(t, worktree, "ci/failure_test.go", "package ci\nfunc broken() {}\n", 0o600)
	runGit(t, worktree, "init", "-b", "agent/janitor-scan")
	runGit(t, worktree, "remote", "add", "origin", "git@github.example:acme/repo.git")
	binDir := t.TempDir()
	gh := `#!/bin/sh
if [ "$1" = auth ] && [ "$2" = status ]; then
	[ "$3" = --active ] && [ "$4" = --hostname ] && [ "$5" = github.example ] || exit 2
	exit
fi
if [ "$1" = run ] && [ "$2" = list ]; then
  [ "$#" -eq 8 ] && [ "$3" = --branch ] && [ "$4" = main ] && [ "$5" = --limit ] && [ "$6" = 100 ] && [ "$7" = --json ] || exit 1
  [ "$8" = databaseId,workflowDatabaseId,workflowName,conclusion,url ] || exit 1
  printf '%s\n' '[{"databaseId":42,"workflowDatabaseId":100,"workflowName":"CI","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/42"},{"databaseId":41,"workflowDatabaseId":100,"workflowName":"CI","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/41"},{"databaseId":43,"workflowDatabaseId":101,"workflowName":"Release","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/43"},{"databaseId":45,"workflowDatabaseId":102,"workflowName":"CI","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/45"},{"databaseId":48,"workflowDatabaseId":103,"workflowName":"Docs","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/48"},{"databaseId":46,"workflowDatabaseId":0,"workflowName":"","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/46"},{"databaseId":47,"workflowDatabaseId":0,"workflowName":"","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/47"}]'
  exit
fi
if [ "$1" = run ] && [ "$2" = view ] && [ "$4" = --json ] && [ "$5" = jobs ]; then
  case "$3" in
    42) printf '%s\n' '{"jobs":[{"name":"unit tests","conclusion":"failure"},{"name":"build","conclusion":"success"}]}' ;;
    43) printf '%s\n' '{"jobs":[{"name":"publish","conclusion":"failure"}]}' ;;
    45) printf '%s\n' '{"jobs":[{"name":"integration tests","conclusion":"failure"}]}' ;;
    48) printf '%s\n' '{"jobs":[{"name":"docs","conclusion":"success"}]}' ;;
    46) printf '%s\n' '{"jobs":[{"name":"ruleset policy A","conclusion":"failure"}]}' ;;
    47) printf '%s\n' '{"jobs":[{"name":"ruleset policy B","conclusion":"failure"}]}' ;;
    *) exit 1 ;;
  esac
  exit
fi
if [ "$1" = run ] && [ "$2" = view ] && [ "$4" = --log-failed ]; then
  printf '%s\n' 'ci/failure_test.go:2:6: compile failure'
  exit
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
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
		File:     "ci/failure_test.go",
		Line:     2,
		Title:    "CI failed",
		Detail:   "workflow: CI; job: unit tests; run: https://github.example/acme/repo/actions/runs/42; evidence: ci/failure_test.go:2:6: compile failure",
	}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
	want = janitor.Candidate{
		Detector: "ci",
		File:     "ci/failure_test.go",
		Line:     2,
		Title:    "Release failed",
		Detail:   "workflow: Release; job: publish; run: https://github.example/acme/repo/actions/runs/43; evidence: ci/failure_test.go:2:6: compile failure",
	}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
	want = janitor.Candidate{
		Detector: "ci",
		File:     "ci/failure_test.go",
		Line:     2,
		Title:    "CI failed",
		Detail:   "workflow: CI; job: integration tests; run: https://github.example/acme/repo/actions/runs/45; evidence: ci/failure_test.go:2:6: compile failure",
	}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want distinct same-name workflow %#v", cands, want)
	}
	want = janitor.Candidate{
		Detector: "ci",
		File:     "ci/failure_test.go",
		Line:     2,
		Title:    "Docs failed",
		Detail:   "workflow: Docs; job: unspecified job; run: https://github.example/acme/repo/actions/runs/48; evidence: ci/failure_test.go:2:6: compile failure",
	}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want no display-title-as-job fallback %#v", cands, want)
	}
	for _, ruleset := range []struct {
		job string
		url string
	}{
		{job: "ruleset policy A", url: "https://github.example/acme/repo/actions/runs/46"},
		{job: "ruleset policy B", url: "https://github.example/acme/repo/actions/runs/47"},
	} {
		want = janitor.Candidate{
			Detector: "ci",
			File:     "ci/failure_test.go",
			Line:     2,
			Title:    "CI workflow failed",
			Detail:   "workflow: CI workflow; job: " + ruleset.job + "; run: " + ruleset.url + "; evidence: ci/failure_test.go:2:6: compile failure",
		}
		if !containsCandidate(cands, want) {
			t.Errorf("candidates = %#v, want zero-workflow-ID fallback %#v", cands, want)
		}
	}
	if got := detectorCandidateCount(cands, "ci"); got != 6 {
		t.Errorf("ci candidate count = %d, want one per failing workflow (6): %#v", got, cands)
	}
}

func TestCIDetectorNoopWhenToolMissing(t *testing.T) {
	worktree := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
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

func TestCIDetectorNoopWithoutOrigin(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	binDir := t.TempDir()
	gh := "#!/bin/sh\necho 'gh must not run without an origin remote' >&2\nexit 9\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if contains(ran, "ci") || !contains(skipped, "ci") {
		t.Errorf("ran, skipped = %#v, %#v, want missing-origin CI skipped", ran, skipped)
	}
	for _, candidate := range cands {
		if candidate.Detector == "ci" {
			t.Errorf("candidates = %#v, want no CI finding without an origin", cands)
		}
	}
}

func TestCIDetectorNoopWhenUnauthed(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "remote", "add", "origin", "ssh://git@github.example/acme/repo.git")
	binDir := t.TempDir()
	gh := "#!/bin/sh\necho 'authentication required' >&2\nexit 4\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
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

func TestCIDetectorReturnsAuthenticatedProbeError(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "remote", "add", "origin", "git@github.example:acme/repo.git")
	binDir := t.TempDir()
	gh := `#!/bin/sh
if [ "$1" = auth ] && [ "$2" = status ]; then
	[ "$3" = --active ] && [ "$4" = --hostname ] && [ "$5" = github.example ] || exit 2
	exit
fi
echo 'CI API unavailable' >&2
exit 4
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
	if err == nil {
		t.Fatal("run built-in detectors succeeded, want authenticated CI probe error")
	}
	if !strings.Contains(err.Error(), "CI API unavailable") {
		t.Errorf("error = %q, want CI probe stderr", err)
	}
	if contains(ran, "ci") || contains(skipped, "ci") {
		t.Errorf("ran, skipped = %#v, %#v, want authenticated CI probe classified as error", ran, skipped)
	}
}

func TestCIDetectorNoopWhenGreen(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "remote", "add", "origin", "https://github.example/acme/repo.git")
	binDir := t.TempDir()
	gh := `#!/bin/sh
if [ "$1" = auth ] && [ "$2" = status ]; then
	[ "$3" = --active ] && [ "$4" = --hostname ] && [ "$5" = github.example ] || exit 2
	exit
fi
[ "$#" -eq 8 ] && [ "$1" = run ] && [ "$2" = list ] && [ "$3" = --branch ] && [ "$4" = main ] && [ "$5" = --limit ] && [ "$6" = 100 ] && [ "$7" = --json ] && [ "$8" = databaseId,workflowDatabaseId,workflowName,conclusion,url ] || exit 1
printf '%s\n' '[{"databaseId":44,"workflowDatabaseId":100,"workflowName":"CI","displayTitle":"unit tests","conclusion":"success","url":"https://github.example/acme/repo/actions/runs/44"},{"databaseId":42,"workflowDatabaseId":100,"workflowName":"CI","displayTitle":"older unit tests","conclusion":"failure","url":"https://github.example/acme/repo/actions/runs/42"}]'
`
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "main", janitor.WithDirectExecutionForTest())
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

func TestCIDetectorNoopWithoutTargetBranch(t *testing.T) {
	binDir := t.TempDir()
	gh := "#!/bin/sh\nprintf '%s\\n' '[]'\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatalf("write gh fixture: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), t.TempDir(), "", janitor.WithDirectExecutionForTest())
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
	if err := os.WriteFile(filepath.Join(worktree, "unused.py"), []byte("def unused(): pass\n"), 0o600); err != nil {
		t.Fatalf("write vulture candidate fixture: %v", err)
	}
	binDir := t.TempDir()
	vulturePath := filepath.Join(binDir, "vulture")
	vulture := "#!/bin/sh\nprintf '%s\\n' 'unused.py:1: unused function'\nexit 1\n"
	if err := os.WriteFile(vulturePath, []byte(vulture), 0o700); err != nil {
		t.Fatalf("write vulture fixture: %v", err)
	}
	t.Setenv("PATH", binDir)

	cands, ran, skipped, err := janitor.RunBuiltins(context.Background(), worktree, "", janitor.WithDirectExecutionForTest())
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "vulture") {
		t.Errorf("ran = %#v, want vulture", ran)
	}
	if contains(skipped, "vulture") {
		t.Errorf("skipped = %#v, should not include vulture", skipped)
	}
	if !reflect.DeepEqual(cands, []janitor.Candidate{{
		Detector: "vulture", File: "unused.py", Line: 1,
		Title: "unused.py:1: unused function", Detail: "unused.py:1: unused function",
	}}) {
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

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree, "", janitor.WithDirectExecutionForTest())
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

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree, "", janitor.WithDirectExecutionForTest())
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
	contents := `package fixture

// TODO(oro-old): remove legacy path
// FIXME remove another legacy path
/*
 * TODO: remove block-comment fallback
 */
-- TODO: remove legacy schema
var examples = []string{"TODO marker", "FIXME marker"}

// Route tokens such as (TODO, handleRequest) through ripgrep.
`
	if err := os.WriteFile(oldPath, []byte(contents), 0o600); err != nil {
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

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree, "", janitor.WithDirectExecutionForTest())
	if err != nil {
		t.Fatalf("run built-in detectors: %v", err)
	}
	if !contains(ran, "todo") {
		t.Errorf("ran = %#v, want todo", ran)
	}
	want := janitor.Candidate{Detector: "todo", File: "old.go", Line: 3, Title: "stale TODO/FIXME", Detail: "// TODO(oro-old): remove legacy path"}
	if !containsCandidate(cands, want) {
		t.Errorf("candidates = %#v, want %#v", cands, want)
	}
	blockWant := janitor.Candidate{Detector: "todo", File: "old.go", Line: 6, Title: "stale TODO/FIXME", Detail: "* TODO: remove block-comment fallback"}
	if !containsCandidate(cands, blockWant) {
		t.Errorf("candidates = %#v, want multiline block candidate %#v", cands, blockWant)
	}
	sqlWant := janitor.Candidate{Detector: "todo", File: "old.go", Line: 8, Title: "stale TODO/FIXME", Detail: "-- TODO: remove legacy schema"}
	if !containsCandidate(cands, sqlWant) {
		t.Errorf("candidates = %#v, want SQL comment candidate %#v", cands, sqlWant)
	}
	if got := detectorCandidateCount(cands, "todo"); got != 4 {
		t.Errorf("TODO candidates = %d, want only the four actionable comment markers; candidates=%#v", got, cands)
	}
}

func TestJanitorBuiltinsFindsBrokenRelativeLinksWithoutTools(t *testing.T) {
	worktree := t.TempDir()
	contents := `[missing](docs/missing.md)
[titled](docs/titled-missing.md "Guide")
[angle](<docs/missing guide.md>)

` + "`[inline example](docs/inline-example.md)`" + `
[malformed](docs/no-closing-paren.md

func RetryOperation[T any](fn func()) {}

` + "```markdown" + `
[example only](docs/example.md)
~~~
` + "```" + `
[after fence](docs/after-fence.md)
`
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write Markdown fixture: %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	cands, ran, _, err := janitor.RunBuiltins(context.Background(), worktree, "", janitor.WithDirectExecutionForTest())
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
	for _, target := range []janitor.Candidate{
		{Detector: "broken-links", File: "README.md", Line: 2, Title: "broken relative link", Detail: "docs/titled-missing.md"},
		{Detector: "broken-links", File: "README.md", Line: 3, Title: "broken relative link", Detail: "docs/missing guide.md"},
		{Detector: "broken-links", File: "README.md", Line: 14, Title: "broken relative link", Detail: "docs/after-fence.md"},
	} {
		if !containsCandidate(cands, target) {
			t.Errorf("candidates = %#v, want %#v", cands, target)
		}
	}
	if got := detectorCandidateCount(cands, "broken-links"); got != 4 {
		t.Errorf("broken-link candidates = %d, want four real Markdown links; candidates=%#v", got, cands)
	}
}

func TestScriptCatalogUsesInterpreterForNonExecutableShellScript(t *testing.T) {
	const scriptPath = "scripts/nilaway_lint_wiring_test.sh"
	repositoryRoot := filepath.Join("..", "..")
	info, err := os.Stat(filepath.Join(repositoryRoot, filepath.FromSlash(scriptPath)))
	if err != nil {
		t.Fatalf("stat %s: %v", scriptPath, err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		return
	}

	catalog, err := os.ReadFile(filepath.Join(repositoryRoot, "scripts", "README.md"))
	if err != nil {
		t.Fatalf("read script catalog: %v", err)
	}
	if !strings.Contains(string(catalog), "`bash "+scriptPath+"`") {
		t.Fatalf("non-executable %s must be invoked through bash in scripts/README.md", scriptPath)
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

func detectorCandidateCount(candidates []janitor.Candidate, detector string) int {
	count := 0
	for _, candidate := range candidates {
		if candidate.Detector == detector {
			count++
		}
	}
	return count
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
