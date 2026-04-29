package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHeadlessDiffTest(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := runCLI([]string{"--headless", "--diff-test"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("runCLI() exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "dashboard diff-test passed") {
		t.Fatalf("stdout missing success message:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestHeadlessDiffTestReportsSnapshotMismatch(t *testing.T) {
	err := compareDashboardSnapshot("not the dashboard snapshot\n")

	if err == nil {
		t.Fatal("compareDashboardSnapshot() error = nil, want mismatch error")
	}
	if !strings.Contains(err.Error(), "dashboard snapshot mismatch") {
		t.Fatalf("error = %q, want snapshot mismatch", err)
	}
}

func TestUnknownFlagFailsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := runCLI([]string{"--does-not-exist"}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("runCLI() exit = 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr missing unknown flag error:\n%s", stderr.String())
	}
}
