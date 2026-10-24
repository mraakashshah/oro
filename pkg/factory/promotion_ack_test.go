package factory //nolint:testpackage // Real Git shims inject acknowledgment loss at private recovery boundaries.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPromotionLostAcknowledgment(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "step timeout"
		if expired {
			name = "objective deadline"
		}
		t.Run(name, func(t *testing.T) {
			spec, state := promotionAckFixture(t)
			if expired {
				promotionAckShortenDeadline(t, spec, state)
			}
			_, restore := promotionAckShim(t, "delayed")
			status, err := Run(t.Context(), state, spec)
			restore()
			promotionAckCompleted(t, spec, state, status, err, 1)
		})
	}
	t.Run("observation unavailable preserves intent", func(t *testing.T) {
		spec, state := promotionAckFixture(t)
		_, restore := promotionAckShim(t, "unavailable")
		status, err := Run(t.Context(), state, spec)
		if err == nil || status.State != "promoting" {
			t.Fatalf("ambiguous effect lost durable intent: %+v, %v", status, err)
		}
		promotionAckEvidence(t, state, spec.ID, "promoting")
		restore()
		status, err = Run(t.Context(), state, spec)
		promotionAckCompleted(t, spec, state, status, err, 1)
	})
	t.Run("caller cancellation preserves intent", func(t *testing.T) {
		spec, state := promotionAckFixture(t)
		marker, restore := promotionAckShim(t, "delayed")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		watcherDone := make(chan struct{})
		go func() {
			defer close(watcherDone)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := os.Stat(marker); err == nil {
						cancel()
						return
					}
				}
			}
		}()
		status, err := Run(ctx, state, spec)
		cancel()
		<-watcherDone
		if !errors.Is(err, context.Canceled) || status.State != "promoting" {
			t.Fatalf("cancelled promotion lost intent: %+v, %v", status, err)
		}
		promotionAckEvidence(t, state, spec.ID, "promoting")
		restore()
		status, err = Run(t.Context(), state, spec)
		promotionAckCompleted(t, spec, state, status, err, 1)
	})
	t.Run("proved no effect retries within budget", func(t *testing.T) {
		spec, state := promotionAckFixture(t)
		spec.MaxAttempts = 2
		_, restore := promotionAckShim(t, "no-effect-once")
		status, err := Run(t.Context(), state, spec)
		restore()
		promotionAckCompleted(t, spec, state, status, err, 2)
	})
}

func promotionAckFixture(t *testing.T) (Spec, string) {
	t.Helper()
	spec, state := engineFixture(t, "repair")
	engineWrite(t, spec.Worker.Path, "#!/bin/sh\ncat >/dev/null\nprintf delivered > delivered.txt\n", 0o700)
	engineWrite(t, spec.Acceptance.Path, "#!/bin/sh\ncat >/dev/null\n[ \"$(cat delivered.txt)\" = delivered ] || exit 1\nsha=$(git rev-parse HEAD)\nprintf '{\"candidate_sha\":\"%s\",\"passed\":true,\"checks\":[\"positive-addition\",\"negative-addition\"]}\\n' \"$sha\"\n", 0o700)
	engineWrite(t, spec.Review.Path, "#!/bin/sh\ncat >/dev/null\nsha=$(git rev-parse HEAD)\nprintf '{\"candidate_sha\":\"%s\",\"approved\":true}\\n' \"$sha\"\n", 0o700)
	spec.TimeoutSeconds, spec.StepTimeoutSeconds, spec.MaxAttempts = 10, 1, 1
	return spec, state
}

func promotionAckShim(t *testing.T, mode string) (string, func()) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "effect")
	script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = update-ref ]; then\n"
	if mode == "no-effect-once" {
		script += "  if [ ! -e " + engineQuote(marker) + " ]; then touch " + engineQuote(marker) + "; exit 71; fi\n  exec " + engineQuote(realGit) + " \"$@\"\n"
	} else {
		script += "  " + engineQuote(realGit) + " \"$@\" || exit $?\n  touch " + engineQuote(marker) + "\n  sleep 3\n  exit 0\n"
	}
	script += " fi\ndone\n"
	if mode == "unavailable" {
		script += "if [ -e " + engineQuote(marker) + " ]; then exit 72; fi\n"
	}
	script += "exec " + engineQuote(realGit) + " \"$@\"\n"
	engineWrite(t, filepath.Join(dir, "git"), script, 0o700)
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+originalPath)
	return marker, func() { t.Setenv("PATH", originalPath) }
}

func promotionAckShortenDeadline(t *testing.T, spec Spec, state string) {
	t.Helper()
	crash := errors.New("stop before CAS")
	if _, err := run(t.Context(), state, spec, recoveryCrashAt("promoting", crash)); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	s := recoverySnapshot(t, state, spec.ID)
	s.Deadline = time.Now().Add(time.Second)
	store, err := openStore(t.Context(), filepath.Join(state, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.save(t.Context(), s, s.Revision); err != nil {
		t.Fatal(err)
	}
}

func promotionAckEvidence(t *testing.T, state, id, want string) {
	t.Helper()
	s := recoverySnapshot(t, state, id)
	if s.State != want || s.CandidateSHA == "" || s.BaseSHA == "" || s.Acceptance == nil || s.Review == nil {
		t.Fatalf("lost promotion identity/evidence: %+v", s)
	}
}

func promotionAckCompleted(t *testing.T, spec Spec, state string, status Status, err error, attempts int) {
	t.Helper()
	actual := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch)
	if err != nil || status.State != "completed" || status.CandidateSHA != actual || status.Attempts != attempts {
		t.Fatalf("actual=%s, promotion status=%+v, error=%v", actual, status, err)
	}
	promotionAckEvidence(t, state, spec.ID, "completed")
	if got := engineGit(t, spec.Repository, "show", actual+":delivered.txt"); got != "delivered" {
		t.Fatalf("wrong promoted artifact: %q", got)
	}
}
