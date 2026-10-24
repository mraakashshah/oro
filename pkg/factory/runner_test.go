package factory //nolint:testpackage // Process invariants exercise the private runner protocol.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoundedExecution(t *testing.T) {
	t.Run("literal argv and one input document", runnerTestProtocol)
	t.Run("private environment", runnerTestEnvironment)
	t.Run("nonzero process", runnerTestFailure)
	t.Run("deadline kills group", runnerTestDeadline)
	t.Run("cancellation kills group", runnerTestCancellation)
	t.Run("stdout overflow", func(t *testing.T) { runnerTestOverflow(t, false) })
	t.Run("stderr overflow", func(t *testing.T) { runnerTestOverflow(t, true) })
	t.Run("descendant holds pipe", runnerTestHeldPipe)
}

func runnerTestPolicy(t *testing.T, body string) Command {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return Command{Path: path}
}

func runnerTestProtocol(t *testing.T) {
	t.Helper()
	worktree := t.TempDir()
	resolvedWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	command := runnerTestPolicy(t, "printf '%s\\n' \"$1\"\npwd\ncat\n")
	command.Args = []string{"$(touch injected); spaced 'argument'"}
	input := commandInput{
		Objective: "quoted \"objective\"", Attempt: 2, Worktree: worktree,
		CandidateSHA: "candidate", BaseSHA: "base", Feedback: "repair\nthis",
	}
	result := executeCommand(t.Context(), command, input, t.TempDir())
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("execute: exit %d, error %v", result.ExitCode, result.Err)
	}
	lines := bytes.SplitN(result.Stdout, []byte("\n"), 3)
	if len(lines) != 3 || string(lines[0]) != command.Args[0] || string(lines[1]) != resolvedWorktree {
		t.Fatalf("argv/cwd protocol mismatch: %q", result.Stdout)
	}
	var got commandInput
	decoder := json.NewDecoder(bytes.NewReader(lines[2]))
	if err := decoder.Decode(&got); err != nil || got != input {
		t.Fatalf("input = %#v, error %v; want %#v", got, err, input)
	}
	if err := decoder.Decode(&got); !errors.Is(err, io.EOF) {
		t.Fatalf("expected exactly one JSON document, got %v", err)
	}
	for _, key := range []string{"objective", "attempt", "worktree", "candidate_sha", "base_sha", "feedback"} {
		if !bytes.Contains(lines[2], []byte(`"`+key+`":`)) {
			t.Errorf("missing input field %q", key)
		}
	}
	if _, err := os.Stat(filepath.Join(worktree, "injected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("argument was evaluated: %v", err)
	}
}

func runnerTestEnvironment(t *testing.T) {
	t.Helper()
	privateDir, worktree := t.TempDir(), t.TempDir()
	command := runnerTestPolicy(t, "env\n")
	for _, key := range []string{"ORO", "ORO_WORKER_ID", "ORO_HOME", "ORO_UNKNOWN", "GIT_DIR", "GIT_CONFIG_COUNT", "GIT_AUTHOR_NAME"} {
		t.Setenv(key, "inherited-identity")
	}
	for _, key := range []string{"TMPDIR", "XDG_CACHE_HOME", "GOCACHE"} {
		t.Setenv(key, "/inherited/shared")
	}
	result := executeCommand(t.Context(), command, commandInput{Worktree: worktree}, privateDir)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	env := make(map[string]string)
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		key, value, _ := strings.Cut(line, "=")
		env[key] = value
		if key == "ORO" || strings.HasPrefix(key, "ORO_") || strings.HasPrefix(key, "GIT_") && value == "inherited-identity" {
			t.Errorf("inherited identity survived: %s", key)
		}
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "GOCACHE"} {
		if !strings.HasPrefix(env[key], privateDir+string(filepath.Separator)) {
			t.Errorf("%s not private: %q", key, env[key])
		}
		if stat, err := os.Stat(env[key]); err != nil || !stat.IsDir() {
			t.Errorf("%s directory not created: %v", key, err)
		}
	}
	for key, want := range map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_EDITOR": "true", "GIT_SEQUENCE_EDITOR": "true", "GIT_ASKPASS": "false", "SSH_ASKPASS": "false"} {
		if env[key] != want {
			t.Errorf("%s = %q, want %q", key, env[key], want)
		}
	}
}

func runnerTestFailure(t *testing.T) {
	t.Helper()
	result := executeCommand(t.Context(), runnerTestPolicy(t, "printf positive\nprintf diagnostic >&2\nexit 17\n"), commandInput{Worktree: t.TempDir()}, t.TempDir())
	if result.Err == nil || result.ExitCode != 17 || string(result.Stdout) != "positive" || string(result.Stderr) != "diagnostic" {
		t.Fatalf("nonzero result lost: %#v", result)
	}
}

func runnerTestDeadline(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	runnerTestGroupStop(t, ctx, nil, context.DeadlineExceeded)
}

func runnerTestCancellation(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runnerTestGroupStop(t, ctx, cancel, context.Canceled)
}

func runnerTestGroupStop(t *testing.T, ctx context.Context, cancel context.CancelFunc, want error) {
	t.Helper()
	worktree := t.TempDir()
	command := runnerTestPolicy(t, "(sleep 1; printf escaped > escaped) &\nprintf ready\nsleep 30\n")
	if cancel != nil {
		timer := time.AfterFunc(400*time.Millisecond, cancel)
		defer timer.Stop()
	}
	started := time.Now()
	result := executeCommand(ctx, command, commandInput{Worktree: worktree}, t.TempDir())
	if !errors.Is(result.Err, want) || result.ExitCode == 0 || string(result.Stdout) != "ready" {
		t.Fatalf("cancellation exit %d, output %q, error %v; want %v", result.ExitCode, result.Stdout, result.Err, want)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancellation did not promptly terminate the process group")
	}
	runnerTestNoEscape(t, worktree)
}

func runnerTestNoEscape(t *testing.T, worktree string) {
	t.Helper()
	timer := time.NewTimer(1100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if _, err := os.Stat(filepath.Join(worktree, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived and wrote its delayed marker: %v", err)
	}
}

func runnerTestOverflow(t *testing.T, stderr bool) {
	t.Helper()
	redirect := ""
	if stderr {
		redirect = " >&2"
	}
	worktree := t.TempDir()
	command := runnerTestPolicy(t, "(sleep 1; printf escaped > escaped) &\nhead -c 1048577 /dev/zero"+redirect+"\nsleep 30\n")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started := time.Now()
	result := executeCommand(ctx, command, commandInput{Worktree: worktree}, t.TempDir())
	if result.Err == nil || !strings.Contains(result.Err.Error(), "output limit") || errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("overflow did not fail immediately: %v", result.Err)
	}
	output := result.Stdout
	if stderr {
		output = result.Stderr
	}
	if len(output) != 1<<20 || len(result.Stdout) > 1<<20 || len(result.Stderr) > 1<<20 {
		t.Fatalf("incorrect output bounds: stdout %d, stderr %d", len(result.Stdout), len(result.Stderr))
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("overflow did not actively terminate the group")
	}
	runnerTestNoEscape(t, worktree)
}

func runnerTestHeldPipe(t *testing.T) {
	t.Helper()
	worktree := t.TempDir()
	command := runnerTestPolicy(t, "(sleep 1; printf escaped > escaped) &\nprintf parent-exited\nexit 0\n")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started := time.Now()
	result := executeCommand(ctx, command, commandInput{Worktree: worktree}, t.TempDir())
	if result.Err == nil || errors.Is(result.Err, context.DeadlineExceeded) || string(result.Stdout) != "parent-exited" {
		t.Fatalf("lingering pipe accepted or waited for deadline: %#v", result)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("lingering pipe was not bounded")
	}
	runnerTestNoEscape(t, worktree)
}
