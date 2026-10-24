package factory //nolint:testpackage // Real Git filters exercise the private subprocess boundary and public lifecycle.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFactoryGitBounds(t *testing.T) {
	t.Run("objective deadline includes checkout", gitBoundsTestObjectiveDeadline)
	t.Run("caller cancellation kills filter descendants", gitBoundsTestCancellation)
	t.Run("stdout limit", gitBoundsTestStdout)
	t.Run("stderr limit kills filter descendants", gitBoundsTestStderr)
	t.Run("promotion effect observed after deadline", gitBoundsTestExpiredPromotionProof)
}

func gitBoundsTestObjectiveDeadline(t *testing.T) {
	t.Helper()
	spec, state := engineFixture(t, "repair")
	markerDir := gitBoundsTestFilter(t, spec.Repository, "(sleep 1.5; printf escaped > \"$1/escaped\") &\nsleep 3\ncat\n")
	spec.TimeoutSeconds, spec.StepTimeoutSeconds = 1, 1
	base := gitTestRun(t, spec.Repository, "rev-parse", "delivery")
	started := time.Now()
	status, err := Run(t.Context(), state, spec)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("one-second objective took %s during Git checkout", elapsed)
	}
	if err != nil || status.State != "failed" || status.Attempts != 1 || !strings.Contains(status.Reason, "deadline") {
		t.Errorf("deadline outcome = %+v, %v; want durable failure after one spent attempt", status, err)
	}
	if actual := gitTestRun(t, spec.Repository, "rev-parse", "delivery"); actual != base {
		t.Errorf("expired objective advanced target to %s", actual)
	}
	runnerTestNoEscape(t, markerDir)
}

func gitBoundsTestCancellation(t *testing.T) {
	t.Helper()
	spec, state := engineFixture(t, "repair")
	markerDir := gitBoundsTestFilter(t, spec.Repository, "(sleep 1; printf escaped > \"$1/escaped\") &\nsleep 3\ncat\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	timer := time.AfterFunc(400*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := Run(ctx, state, spec)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation returned %v, want wrapped caller cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("caller cancellation waited %s for a Git descendant pipe", elapsed)
	}
	runnerTestNoEscape(t, markerDir)
}

func gitBoundsTestStdout(t *testing.T) {
	t.Helper()
	repo, _ := gitTestRepository(t)
	gitTestWrite(t, filepath.Join(repo, "large"), strings.Repeat("x", commandOutputLimit+1), 0o600)
	gitTestRun(t, repo, "add", "large")
	gitTestRun(t, repo, "commit", "-m", "large source blob")
	git := gitRepository{path: repo}
	out, err := git.run(t.Context(), repo, "show", "HEAD:large")
	if err == nil || !strings.Contains(err.Error(), "stdout output limit") || out != "" {
		t.Fatalf("oversized Git stdout accepted: length=%d, error=%v", len(out), err)
	}
	if len(err.Error()) > commandOutputLimit+4096 {
		t.Fatalf("Git error retained unbounded stdout: %d bytes", len(err.Error()))
	}
}

func gitBoundsTestStderr(t *testing.T) {
	t.Helper()
	repo, _ := gitTestRepository(t)
	markerDir := gitBoundsTestFilter(t, repo, "(sleep 1; printf escaped > \"$1/escaped\") &\nprintf 'filter diagnostic\\n' >&2\nhead -c 1048577 /dev/zero >&2\nsleep 3\ncat\n")
	git := gitRepository{path: repo}
	base := gitTestRun(t, repo, "rev-parse", "delivery")
	started := time.Now()
	err := git.checkout(t.Context(), filepath.Join(t.TempDir(), "checkout"), base)
	if err == nil || !strings.Contains(err.Error(), "stderr output limit") || !strings.Contains(err.Error(), "filter diagnostic") {
		t.Errorf("excessive filter output not rejected with diagnostics: %v", err)
	} else if len(err.Error()) > commandOutputLimit+4096 {
		t.Errorf("Git error retained unbounded stderr: %d bytes", len(err.Error()))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("Git output overflow waited %s for descendants", elapsed)
	}
	runnerTestNoEscape(t, markerDir)
}

func gitBoundsTestExpiredPromotionProof(t *testing.T) {
	t.Helper()
	spec, state := engineFixture(t, "repair")
	// Shell adapters avoid timing dependence on race-instrumented Go child exits.
	engineWrite(t, spec.Worker.Path, "#!/bin/sh\ncat >/dev/null\nprintf delivered > delivered.txt\n", 0o700)
	engineWrite(t, spec.Acceptance.Path, "#!/bin/sh\nset -eu\ncat >/dev/null\ntest -f delivered.txt\nsha=$(git rev-parse HEAD)\nprintf '{\"candidate_sha\":\"%s\",\"passed\":true,\"checks\":[\"positive-addition\",\"negative-addition\"]}\\n' \"$sha\"\n", 0o700)
	engineWrite(t, spec.Review.Path, "#!/bin/sh\ncat >/dev/null\nsha=$(git rev-parse HEAD)\nprintf '{\"candidate_sha\":\"%s\",\"approved\":true}\\n' \"$sha\"\n", 0o700)
	spec.TimeoutSeconds, spec.StepTimeoutSeconds = 3, 1
	status, err := run(t.Context(), state, spec, runHooks{afterPromotion: func(s snapshot) error {
		// Delay observation of an already-authorized effect beyond its deadline.
		timer := time.NewTimer(time.Until(s.Deadline.Add(20 * time.Millisecond)))
		defer timer.Stop()
		<-timer.C
		return nil
	}})
	if err != nil || status.State != "completed" {
		t.Fatalf("failed to prove authorized effect after deadline: %+v, %v", status, err)
	}
	if got := gitTestRun(t, spec.Repository, "rev-parse", spec.TargetBranch); got != status.CandidateSHA {
		t.Fatalf("completed candidate %s differs from actual %s", status.CandidateSHA, got)
	}
}

func gitBoundsTestFilter(t *testing.T, repo, body string) string {
	t.Helper()
	gitTestWrite(t, filepath.Join(repo, ".gitattributes"), "source.txt filter=factory-bound\n", 0o600)
	gitTestWrite(t, filepath.Join(repo, "source.txt"), "filtered source\n", 0o600)
	gitTestRun(t, repo, "add", ".gitattributes", "source.txt")
	gitTestRun(t, repo, "commit", "-m", "source with filter")
	gitTestRun(t, repo, "branch", "-f", "delivery", "HEAD")
	dir := t.TempDir()
	filter := filepath.Join(dir, "filter")
	gitTestWrite(t, filter, "#!/bin/sh\n"+body, 0o700)
	gitTestRun(t, repo, "config", "filter.factory-bound.smudge", gitTestShellQuote(filter)+" "+gitTestShellQuote(dir))
	return dir
}
