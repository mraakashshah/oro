package processenv_test

import (
	"testing"

	"oro/pkg/processenv"
)

func TestForWorkdirNormalizesPWDAndStripsGitEnv(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"PWD=/wrong/root",
		"GIT_DIR=/wrong/root/.git",
		"GIT_WORK_TREE=/wrong/root",
		"GIT_COMMON_DIR=/wrong/root/.git",
		"GIT_INDEX_FILE=/wrong/index",
		"GIT_PREFIX=cmd/oro/",
		"CUSTOM=1",
	}

	got := processenv.ForWorkdir(env, "/assigned/worktree")
	wantPresent := map[string]bool{
		"PATH=/bin":              false,
		"PWD=/assigned/worktree": false,
		"CUSTOM=1":               false,
	}
	for _, entry := range got {
		if _, ok := wantPresent[entry]; ok {
			wantPresent[entry] = true
		}
		if entry == "GIT_DIR=/wrong/root/.git" ||
			entry == "GIT_WORK_TREE=/wrong/root" ||
			entry == "GIT_COMMON_DIR=/wrong/root/.git" ||
			entry == "GIT_INDEX_FILE=/wrong/index" ||
			entry == "GIT_PREFIX=cmd/oro/" {
			t.Fatalf("git override env leaked: %q in %v", entry, got)
		}
	}
	for entry, found := range wantPresent {
		if !found {
			t.Fatalf("missing %q in %v", entry, got)
		}
	}
}

func TestForWorkdirAddsPWDWhenMissing(t *testing.T) {
	got := processenv.ForWorkdir([]string{"PATH=/bin"}, "/assigned/worktree")
	if len(got) != 2 || got[1] != "PWD=/assigned/worktree" {
		t.Fatalf("ForWorkdir() = %v, want PWD appended", got)
	}
}
