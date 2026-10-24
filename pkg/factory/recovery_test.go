package factory //nolint:testpackage // Crash injection is private and never exposed by the CLI.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestartPromotion(t *testing.T) {
	crash := errors.New("simulated controller interruption")
	t.Run("effect precedes durable completion", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		_, err := run(t.Context(), state, spec, runHooks{afterPromotion: func(snapshot) error { return crash }})
		if !errors.Is(err, crash) {
			t.Fatalf("did not interrupt after effect: %v", err)
		}
		s := recoverySnapshot(t, state, spec.ID)
		if s.State != "promoting" || engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch) != s.CandidateSHA {
			t.Fatalf("effect/intent window not established: %+v", s)
		}
		recoveryExpire(t, state, s)
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "completed" || status.Attempts != 2 || status.CandidateSHA != s.CandidateSHA {
			t.Fatalf("reconciliation after expired deadline: %+v, %v", status, err)
		}
	})
	t.Run("intent precedes effect", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		initial := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch)
		_, err := run(t.Context(), state, spec, recoveryCrashAt("promoting", crash))
		if !errors.Is(err, crash) || engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch) != initial {
			t.Fatalf("did not interrupt before effect: %v", err)
		}
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "completed" || status.Attempts != 2 {
			t.Fatalf("resume should reuse valid approval: %+v, %v", status, err)
		}
	})
	t.Run("intervening target advance survives", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		_, err := run(t.Context(), state, spec, recoveryCrashAt("promoting", crash))
		if !errors.Is(err, crash) {
			t.Fatal(err)
		}
		s := recoverySnapshot(t, state, spec.ID)
		other := filepath.Join(t.TempDir(), "other")
		engineGit(t, spec.Repository, "worktree", "add", "--detach", other, s.BaseSHA)
		engineWrite(t, filepath.Join(other, "external.txt"), "preserve me", 0o600)
		engineGit(t, other, "add", "external.txt")
		engineGit(t, other, "-c", "user.name=Other", "-c", "user.email=other@example.invalid", "commit", "-m", "external advance")
		external := engineGit(t, other, "rev-parse", "HEAD")
		engineGit(t, spec.Repository, "update-ref", "refs/heads/"+spec.TargetBranch, external, s.BaseSHA)
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "completed" || status.Attempts != 3 {
			t.Fatalf("contention recovery: %+v, %v", status, err)
		}
		engineGit(t, spec.Repository, "merge-base", "--is-ancestor", external, status.CandidateSHA)
		if engineGit(t, spec.Repository, "show", status.CandidateSHA+":external.txt") != "preserve me" {
			t.Fatal("intervening contribution lost")
		}
	})
	t.Run("stale policy cannot promote", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		initial := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch)
		_, err := run(t.Context(), state, spec, recoveryCrashAt("approved", crash))
		if !errors.Is(err, crash) {
			t.Fatal(err)
		}
		path := filepath.Join(state, spec.ID, "acceptance")
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		engineWrite(t, path, "#!/bin/sh\nexit 0\n", 0o700)
		if _, err := Run(t.Context(), state, spec); err == nil {
			t.Fatal("tampered frozen policy accepted")
		}
		if engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch) != initial {
			t.Fatal("stale policy promoted candidate")
		}
	})
	t.Run("restart keeps spent attempts and deadline", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		_, err := run(t.Context(), state, spec, recoveryCrashAt("running", crash))
		if !errors.Is(err, crash) {
			t.Fatal(err)
		}
		s := recoverySnapshot(t, state, spec.ID)
		recoveryExpire(t, state, s)
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "failed" || status.Attempts != 1 || !strings.Contains(status.Reason, "deadline") {
			t.Fatalf("restart extended budget/deadline: %+v, %v", status, err)
		}
	})
	for _, phase := range []string{"pending", "running", "candidate", "accepted", "approved", "completed"} {
		t.Run("resume "+phase, func(t *testing.T) {
			spec, state := engineFixture(t, "repair")
			_, err := run(t.Context(), state, spec, recoveryCrashAt(phase, crash))
			if !errors.Is(err, crash) {
				t.Fatalf("phase %s was not persisted: %v", phase, err)
			}
			status, err := Run(t.Context(), state, spec)
			if err != nil || status.State != "completed" || status.Attempts > spec.MaxAttempts {
				t.Fatalf("resume %s: %+v, %v", phase, status, err)
			}
		})
	}
}

func recoveryCrashAt(state string, crash error) runHooks {
	return runHooks{afterTransition: func(s snapshot) error {
		if s.State == state {
			return crash
		}
		return nil
	}}
}

func recoverySnapshot(t *testing.T, state, id string) snapshot {
	t.Helper()
	s, err := readSnapshot(t.Context(), filepath.Join(state, id))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func recoveryExpire(t *testing.T, state string, s snapshot) {
	t.Helper()
	store, err := openStore(t.Context(), filepath.Join(state, s.Spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	s.Deadline = time.Now().Add(-time.Second)
	if err := store.save(t.Context(), s, s.Revision); err != nil {
		t.Fatal(err)
	}
}
