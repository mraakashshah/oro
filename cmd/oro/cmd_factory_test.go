package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/factory"
)

func TestFactoryCLI(t *testing.T) {
	t.Run("registered help and inert construction", factoryCLITestHelp)
	t.Run("completed no-change artifact and read-only status", factoryCLITestSuccess)
	t.Run("durable budget failure returns nonzero", factoryCLITestFailure)
	t.Run("strict input and explicit flags", factoryCLITestInputs)
	t.Run("missing status creates nothing", factoryCLITestMissingStatus)
}

func factoryCLITestHelp(t *testing.T) {
	t.Helper()
	scratch := t.TempDir()
	t.Chdir(scratch)
	newRootCmd()
	for _, args := range [][]string{{"factory", "--help"}, {"factory", "run", "--help"}, {"factory", "status", "--help"}} {
		out, _, err := executeCommand(args...)
		if err != nil {
			t.Fatalf("help %v: %v", args, err)
		}
		if !strings.Contains(strings.ToLower(out), "experimental") || !strings.Contains(strings.ToLower(out), "trusted") {
			t.Fatalf("help omits execution envelope: %s", out)
		}
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("constructing commands or requesting help performed filesystem writes: %v, %v", entries, err)
	}
}

func factoryCLITestSuccess(t *testing.T) {
	t.Helper()
	spec, specPath, stateDir := factoryCLITestFixture(t)
	base := factoryCLITestGit(t, spec.Repository, "rev-parse", "delivery")
	out, stderr, err := executeCommand("factory", "run", "--spec", specPath, "--state-dir", stateDir)
	if err != nil || stderr != "" {
		t.Fatalf("factory run: %v\nstderr: %s\nstdout: %s", err, stderr, out)
	}
	status := factoryCLITestStatus(t, out)
	if status.State != "completed" || status.Attempts != 1 || status.CandidateSHA != base || status.ID != spec.ID {
		t.Fatalf("completed status = %+v, want independently checked no-change candidate", status)
	}
	delivered := filepath.Join(t.TempDir(), "delivered")
	factoryCLITestGit(t, spec.Repository, "worktree", "add", delivered, "delivery")
	artifact := exec.Command("/bin/sh", "artifact.sh")
	artifact.Dir = delivered
	result, err := artifact.CombinedOutput()
	if err != nil || strings.TrimSpace(string(result)) != "42" {
		t.Fatalf("delivered artifact = %q, %v", result, err)
	}
	// Reading status must not load or execute changed policies, or need a writable target.
	factoryCLITestWrite(t, spec.Worker.Path, "#!/bin/sh\nexit 91\n", 0o700)
	before := factoryCLITestFiles(t, stateDir)
	readOut, _, err := executeCommand("factory", "status", "--id", spec.ID, "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("read existing status: %v", err)
	}
	if got := factoryCLITestStatus(t, readOut); got != status {
		t.Fatalf("status changed objective: got %+v, want %+v", got, status)
	}
	if after := factoryCLITestFiles(t, stateDir); after != before {
		t.Fatalf("status command changed durable files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func factoryCLITestFailure(t *testing.T) {
	t.Helper()
	spec, specPath, stateDir := factoryCLITestFixture(t)
	factoryCLITestWrite(t, spec.Worker.Path, "#!/bin/sh\ncat >/dev/null\nprintf 'deliberate worker failure' >&2\nexit 7\n", 0o700)
	out, _, err := executeCommand("factory", "run", "--spec", specPath, "--state-dir", stateDir)
	if err == nil {
		t.Fatal("durably failed objective returned success")
	}
	status := factoryCLITestStatus(t, out)
	if status.State != "failed" || status.Attempts != spec.MaxAttempts || status.Reason == "" {
		t.Fatalf("failure status = %+v", status)
	}
	readOut, _, readErr := executeCommand("factory", "status", "--id", spec.ID, "--state-dir", stateDir)
	if readErr != nil || factoryCLITestStatus(t, readOut) != status {
		t.Fatalf("read failed status = %q, %v; want durable receipt", readOut, readErr)
	}
	if got := factoryCLITestGit(t, spec.Repository, "rev-parse", "delivery"); got != factoryCLITestGit(t, spec.Repository, "rev-parse", "main") {
		t.Fatalf("failed objective promoted target: %s", got)
	}
}

func factoryCLITestInputs(t *testing.T) {
	t.Helper()
	_, specPath, stateDir := factoryCLITestFixture(t)
	valid, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"unknown field":    strings.TrimSuffix(string(valid), "}") + ",\"unknown\":true}",
		"trailing object":  string(valid) + " {}",
		"trailing garbage": string(valid) + " invalid",
	} {
		t.Run(name, func(t *testing.T) {
			factoryCLITestWrite(t, specPath, input, 0o600)
			out, _, err := executeCommand("factory", "run", "--spec", specPath, "--state-dir", stateDir)
			if err == nil || out != "" {
				t.Fatalf("invalid input returned %q, %v", out, err)
			}
		})
	}
	factoryCLITestWrite(t, specPath, string(valid), 0o600)
	for _, args := range [][]string{
		{"factory", "run"},
		{"factory", "run", "--spec", specPath},
		{"factory", "run", "--state-dir", stateDir},
		{"factory", "run", "--spec", specPath, "--state-dir", stateDir, "unexpected"},
		{"factory", "status", "--id", "fixture"},
		{"factory", "status", "--state-dir", stateDir},
		{"factory", "status", "--id", "fixture", "--state-dir", stateDir, "unexpected"},
	} {
		if out, _, err := executeCommand(args...); err == nil || out != "" {
			t.Errorf("invalid flags/args %v returned %q, %v", args, out, err)
		}
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("invalid admission created state: %v", err)
	}
}

func factoryCLITestMissingStatus(t *testing.T) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "missing-state")
	out, _, err := executeCommand("factory", "status", "--id", "missing", "--state-dir", stateDir)
	if err == nil || out != "" {
		t.Fatalf("missing status returned %q, %v", out, err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("status created absent state directory: %v", err)
	}
}

func factoryCLITestFixture(t *testing.T) (factory.Spec, string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	factoryCLITestGit(t, repo, "init", "-b", "main")
	factoryCLITestWrite(t, filepath.Join(repo, "artifact.sh"), "#!/bin/sh\nprintf '42\\n'\n", 0o700)
	factoryCLITestGit(t, repo, "add", ".")
	factoryCLITestGit(t, repo, "commit", "-m", "runnable artifact")
	factoryCLITestGit(t, repo, "branch", "delivery")
	worker, accept, review := filepath.Join(root, "worker"), filepath.Join(root, "accept"), filepath.Join(root, "review")
	factoryCLITestWrite(t, worker, "#!/bin/sh\ncat >/dev/null\nexit 0\n", 0o700)
	factoryCLITestWrite(t, accept, "#!/bin/sh\nset -eu\ncat >/dev/null\n[ \"$(/bin/sh artifact.sh)\" = 42 ]\nsha=$(git rev-parse HEAD)\nprintf '{\"candidate_sha\":\"%s\",\"passed\":true,\"checks\":[\"artifact-output\"]}\\n' \"$sha\"\n", 0o700)
	factoryCLITestWrite(t, review, "#!/bin/sh\nset -eu\ncat >/dev/null\nsha=$(git rev-parse HEAD)\nprintf '{\"candidate_sha\":\"%s\",\"approved\":true}\\n' \"$sha\"\n", 0o700)
	spec := factory.Spec{
		ID: "cli-fixture", Objective: "deliver runnable artifact", Repository: repo, TargetBranch: "delivery",
		Worker: factory.Command{Path: worker}, Acceptance: factory.Command{Path: accept}, Review: factory.Command{Path: review},
		RequiredChecks: []string{"artifact-output"}, MaxAttempts: 2, TimeoutSeconds: 30, StepTimeoutSeconds: 5,
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "spec.json")
	factoryCLITestWrite(t, specPath, string(data), 0o600)
	return spec, specPath, filepath.Join(root, "state")
}

func factoryCLITestStatus(t *testing.T, output string) factory.Status {
	t.Helper()
	var status factory.Status
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatalf("status output %q: %v", output, err)
	}
	return status
}

func factoryCLITestFiles(t *testing.T, root string) string {
	t.Helper()
	var contents strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// SQLite read-only WAL connections still create read-coordination files.
		if entry.Name() == "state.db-shm" || (entry.Name() == "state.db-wal" && len(data) == 0) {
			return nil
		}
		fmt.Fprintf(&contents, "%s %x\n", path, sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return contents.String()
}

func factoryCLITestGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	argv := append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Dir = path
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "ORO_") && !strings.HasPrefix(entry, "ORO=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func factoryCLITestWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
