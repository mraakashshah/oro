//nolint:testpackage // Exercises the actual private controller loop.
package factory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type interruptionBackend struct {
	deliveryBackend
	stage    string
	err      error
	calls    []string
	deadline time.Time
}

func (b *interruptionBackend) call(stage string, deadline time.Time) error {
	b.calls = append(b.calls, stage)
	if !deadline.IsZero() && !deadline.Equal(b.deadline) {
		return errors.New("changed deadline")
	}
	if b.stage == stage {
		return b.err
	}
	return nil
}

func (b *interruptionBackend) target(_ context.Context, d time.Time) (string, error) {
	err := b.call("target", d)
	return "base-sha", err
}

func (b *interruptionBackend) checkout(_ context.Context, _ string, _ int, _ string, d time.Time) (string, error) {
	err := b.call("checkout", d)
	return "opaque-workspace", err
}
func (b *interruptionBackend) verifyPolicies() error { return b.call("verify", time.Time{}) }
func (b *interruptionBackend) execute(_ context.Context, _ string, _ commandInput, d time.Time) commandResult {
	err := b.call("execute", d)
	if err != nil {
		return commandResult{Err: err, ExitCode: 17, Stdout: []byte("worker output"), Stderr: []byte("worker diagnostic")}
	}
	return commandResult{ExitCode: 0}
}

func (b *interruptionBackend) commit(_ context.Context, _, _ string, d time.Time) (string, error) {
	err := b.call("commit", d)
	return "candidate", err
}

func TestManagedInterruption(t *testing.T) {
	for _, stage := range []string{"target", "checkout", "verify", "execute", "commit"} {
		t.Run(stage, func(t *testing.T) {
			c, base := backendControllerFixture(t)
			initial := c.state
			initial.Feedback = "prior feedback"
			if err := c.save(t.Context(), initial); err != nil {
				t.Fatal(err)
			}
			initial = c.state
			cause := errors.New("trusted unavailable")
			marker := &managedInterruptionError{stage: stage, cause: cause}
			wrapped := fmt.Errorf("outer: %w", errors.Join(errors.New("other"), marker))
			b := &interruptionBackend{deliveryBackend: base, stage: stage, err: wrapped, deadline: initial.Deadline}
			c.backend = b
			err := c.drive(t.Context())
			var got *managedInterruptionError
			if !errors.As(err, &got) || got != marker || !errors.Is(err, cause) {
				t.Fatalf("lost interruption: %v", err)
			}
			if stage == "execute" && (!strings.Contains(err.Error(), "policy exited 17") || !strings.Contains(err.Error(), "worker output") || !strings.Contains(err.Error(), "worker diagnostic")) {
				t.Fatalf("lost execution diagnostics: %v", err)
			}
			want := map[string][]string{"target": {"target"}, "checkout": {"target", "checkout"}, "verify": {"target", "checkout", "verify"}, "execute": {"target", "checkout", "verify", "execute"}, "commit": {"target", "checkout", "verify", "execute", "commit"}}[stage]
			if !reflect.DeepEqual(b.calls, want) {
				t.Fatalf("effects: %v want %v", b.calls, want)
			}
			state, err := c.store.load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if state.State != "running" || state.Attempts != 1 || state.Feedback != initial.Feedback || !state.CreatedAt.Equal(initial.CreatedAt) || !state.Deadline.Equal(initial.Deadline) || state.PolicyHash != initial.PolicyHash || !reflect.DeepEqual(state.Spec, initial.Spec) {
				t.Fatalf("state lost: %+v", state)
			}
			if stage == "target" || stage == "checkout" {
				if state.BaseSHA != "" || state.Worktree != "" {
					t.Fatalf("premature workspace: %+v", state)
				}
			} else if state.BaseSHA != "base-sha" || state.Worktree != "opaque-workspace" {
				t.Fatalf("workspace lost: %+v", state)
			}
			expectedRevision := initial.Revision + 1
			if stage != "target" && stage != "checkout" {
				expectedRevision++
			}
			if state.Revision != expectedRevision {
				t.Fatalf("unexpected transition revision %d want %d", state.Revision, expectedRevision)
			}
			var events int
			if err := c.store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM events").Scan(&events); err != nil || events != int(expectedRevision) {
				t.Fatalf("events=%d err=%v", events, err)
			}
		})
	}
	for _, stage := range []string{"target", "checkout", "execute", "commit"} {
		t.Run("ordinary "+stage, func(t *testing.T) {
			c, base := backendControllerFixture(t)
			b := &interruptionBackend{deliveryBackend: base, stage: stage, err: errors.New("ordinary"), deadline: c.state.Deadline}
			c.backend = b
			if err := c.drive(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, err := c.store.load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if state.State != "failed" {
				t.Fatalf("ordinary state=%s", state.State)
			}
			want := c.state.Spec.MaxAttempts
			if stage == "target" {
				want = 1
			}
			if state.Attempts != want {
				t.Fatalf("attempts=%d want=%d", state.Attempts, want)
			}
		})
	}
	for _, kind := range []string{"expired", "exhausted"} {
		t.Run(kind, func(t *testing.T) {
			c, base := backendControllerFixture(t)
			next := c.state
			if kind == "expired" {
				next.Deadline = time.Now().Add(-time.Second)
			} else {
				next.Attempts = next.Spec.MaxAttempts
			}
			if err := c.save(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			b := &interruptionBackend{deliveryBackend: base, deadline: next.Deadline}
			c.backend = b
			if err := c.drive(t.Context()); err != nil {
				t.Fatal(err)
			}
			if c.state.State != "failed" || len(b.calls) != 0 {
				t.Fatalf("bounded failure: %s %v", c.state.State, b.calls)
			}
		})
	}
}
