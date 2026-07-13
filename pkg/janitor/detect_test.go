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
