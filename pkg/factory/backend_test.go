package factory //nolint:testpackage // Verify the real controller loop through its private effect boundary.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestControllerBackendPreservesLegacy(t *testing.T) {
	c, backend := backendControllerFixture(t)
	if err := c.drive(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.state.State != "completed" || c.state.Attempts != 2 {
		t.Fatalf("delivery changed: %+v", snapshotStatus(c.state))
	}
	if got := engineGit(t, c.state.Spec.Repository, "rev-parse", c.state.Spec.TargetBranch); got != c.state.CandidateSHA {
		t.Fatalf("target %s differs from accepted artifact %s", got, c.state.CandidateSHA)
	}
	verifyBackendEffects(t, c, backend)
	// An elapsed objective bound must cancel execution even when a fresh step
	// allowance would permit another command.
	result := backend.deliveryBackend.execute(t.Context(), "worker", commandInput{Worktree: backend.workspaces["backend://worker/2"]}, time.Now().Add(-time.Second))
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("backend refreshed an elapsed work deadline: %v", result.Err)
	}
	verifyBackendObservationFailures(t, c, backend)
}

func backendControllerFixture(t *testing.T) (*controller, *recordingDeliveryBackend) {
	t.Helper()
	spec, stateDir := engineFixture(t, "repair")
	policies, err := loadPolicies(stateDir, spec)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockObjective(policies.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unlock() })
	store, err := openStore(t.Context(), policies.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	backend := &recordingDeliveryBackend{deliveryBackend: newLegacyDeliveryBackend(policies)}
	c := &controller{store: store, policies: policies, backend: backend}
	if err := c.admit(t.Context()); err != nil {
		t.Fatal(err)
	}
	backend.deadline = c.state.Deadline
	return c, backend
}

func verifyBackendEffects(t *testing.T, c *controller, backend *recordingDeliveryBackend) {
	t.Helper()
	for _, effect := range []string{"freeze", "verify", "target", "checkout-worker", "checkout-acceptance", "checkout-review", "execute-worker", "execute-acceptance", "execute-review", "pristine", "commit", "exists", "promote", "observe", "completed"} {
		if backend.calls[effect] == 0 {
			t.Errorf("controller bypassed backend effect %s", effect)
		}
	}
	if backend.calls["promote"] != 1 || backend.calls["completed"] != 1 {
		t.Fatal("delivery effects repeated", backend.calls)
	}
	if backend.badDeadline {
		t.Fatal("controller changed the original work deadline")
	}
	if !strings.HasPrefix(c.state.Worktree, "backend://") {
		t.Fatal("controller replaced the backend workspace identity")
	}
}

func verifyBackendObservationFailures(t *testing.T, c *controller, backend *recordingDeliveryBackend) {
	t.Helper()
	// A failed terminal observation must stay an error; the controller cannot
	// reconstruct a host Git observer to turn it into success.
	sentinel := errors.New("backend observation unavailable")
	c.state.Deadline = time.Now().Add(-time.Second)
	backend.observerErr = sentinel
	before := backend.calls["completed"]
	if err := c.drive(t.Context()); !errors.Is(err, sentinel) || backend.calls["completed"] != before+1 {
		t.Fatalf("terminal backend failure lost: %v", err)
	}
	c.state.State = "promoting"
	before = backend.calls["observe"]
	if err := c.drive(t.Context()); !errors.Is(err, sentinel) || c.state.State != "promoting" || backend.calls["observe"] != before+1 {
		t.Fatalf("recovery observation bypassed backend: %v state=%s", err, c.state.State)
	}
	missing := controller{}
	if err := missing.drive(t.Context()); err == nil {
		t.Fatal("missing backend acquired a legacy fallback")
	}
	if err := missing.admit(t.Context()); err == nil {
		t.Fatal("missing backend admitted legacy effects")
	}
}

type recordingDeliveryBackend struct {
	deliveryBackend
	calls       map[string]int
	deadline    time.Time
	badDeadline bool
	observerErr error
	workspaces  map[string]string
}

func (b *recordingDeliveryBackend) record(effect string, deadline time.Time) {
	if b.calls == nil {
		b.calls = make(map[string]int)
	}
	b.calls[effect]++
	switch effect {
	case "verify", "freeze", "observe", "completed":
		return
	}
	if !deadline.Equal(b.deadline) {
		b.badDeadline = true
	}
}

func (b *recordingDeliveryBackend) verifyPolicies() error {
	b.record("verify", time.Time{})
	return b.deliveryBackend.verifyPolicies()
}

func (b *recordingDeliveryBackend) freezePolicies() error {
	b.record("freeze", time.Time{})
	return b.deliveryBackend.freezePolicies()
}

func (b *recordingDeliveryBackend) target(ctx context.Context, deadline time.Time) (string, error) {
	b.record("target", deadline)
	return b.deliveryBackend.target(ctx, deadline)
}

func (b *recordingDeliveryBackend) checkout(ctx context.Context, stage string, attempt int, sha string, deadline time.Time) (string, error) {
	b.record("checkout-"+stage, deadline)
	path, err := b.deliveryBackend.checkout(ctx, stage, attempt, sha, deadline)
	if err != nil {
		return "", err
	}
	if b.workspaces == nil {
		b.workspaces = make(map[string]string)
	}
	identity := fmt.Sprintf("backend://%s/%d", stage, attempt)
	b.workspaces[identity] = path
	return identity, nil
}

func (b *recordingDeliveryBackend) pristine(ctx context.Context, workspace, sha string, deadline time.Time) error {
	b.record("pristine", deadline)
	return b.deliveryBackend.pristine(ctx, b.workspaces[workspace], sha, deadline)
}

func (b *recordingDeliveryBackend) execute(ctx context.Context, stage string, input commandInput, deadline time.Time) commandResult {
	b.record("execute-"+stage, deadline)
	input.Worktree = b.workspaces[input.Worktree]
	return b.deliveryBackend.execute(ctx, stage, input, deadline)
}

func (b *recordingDeliveryBackend) commit(ctx context.Context, workspace, base string, deadline time.Time) (string, error) {
	b.record("commit", deadline)
	return b.deliveryBackend.commit(ctx, b.workspaces[workspace], base, deadline)
}

func (b *recordingDeliveryBackend) exists(ctx context.Context, sha string, deadline time.Time) error {
	b.record("exists", deadline)
	return b.deliveryBackend.exists(ctx, sha, deadline)
}

func (b *recordingDeliveryBackend) promote(ctx context.Context, base, candidate string, deadline time.Time) error {
	b.record("promote", deadline)
	return b.deliveryBackend.promote(ctx, base, candidate, deadline)
}

func (b *recordingDeliveryBackend) observeTarget(ctx context.Context) (string, error) {
	b.record("observe", time.Time{})
	if b.observerErr != nil {
		return "", b.observerErr
	}
	return b.deliveryBackend.observeTarget(ctx)
}

func (b *recordingDeliveryBackend) completedArtifact(ctx context.Context, sha string) error {
	b.record("completed", time.Time{})
	if b.observerErr != nil {
		return b.observerErr
	}
	return b.deliveryBackend.completedArtifact(ctx, sha)
}
