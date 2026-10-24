package factory

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// deliveryBackend owns every external delivery effect. The controller owns
// durable transitions and evidence decisions; it cannot select a host fallback.
type deliveryBackend interface {
	verifyPolicies() error
	freezePolicies() error
	target(context.Context, time.Time) (string, error)
	checkout(context.Context, string, int, string, time.Time) (string, error)
	pristine(context.Context, string, string, time.Time) error
	execute(context.Context, string, commandInput, time.Time) commandResult
	commit(context.Context, string, string, time.Time) (string, error)
	exists(context.Context, string, time.Time) error
	promote(context.Context, string, string, time.Time) error
	observeTarget(context.Context) (string, error)
	completedArtifact(context.Context, string) error
}

type legacyDeliveryBackend struct{ policies policySet }

func newLegacyDeliveryBackend(policies policySet) *legacyDeliveryBackend {
	return &legacyDeliveryBackend{policies: policies}
}

func (b *legacyDeliveryBackend) repository(deadline time.Time) gitRepository {
	return gitRepository{path: b.policies.Spec.Repository, stepTimeout: time.Duration(b.policies.Spec.StepTimeoutSeconds) * time.Second, deadline: deadline}
}

func (b *legacyDeliveryBackend) verifyPolicies() error { return verifyPolicies(b.policies) }
func (b *legacyDeliveryBackend) freezePolicies() error { return freezePolicies(b.policies) }
func (b *legacyDeliveryBackend) target(ctx context.Context, deadline time.Time) (string, error) {
	return b.repository(deadline).target(ctx, b.policies.Spec.TargetBranch)
}

func (b *legacyDeliveryBackend) checkout(ctx context.Context, stage string, attempt int, sha string, deadline time.Time) (string, error) {
	path := filepath.Join(b.policies.Dir, fmt.Sprintf("attempt-%d", attempt), stage)
	return path, b.repository(deadline).checkout(ctx, path, sha)
}

func (b *legacyDeliveryBackend) pristine(ctx context.Context, workspace, sha string, deadline time.Time) error {
	return b.repository(deadline).pristine(ctx, workspace, sha)
}

func (b *legacyDeliveryBackend) execute(ctx context.Context, stage string, input commandInput, deadline time.Time) commandResult {
	stepDeadline := time.Now().Add(time.Duration(b.policies.Spec.StepTimeoutSeconds) * time.Second)
	if deadline.Before(stepDeadline) {
		stepDeadline = deadline
	}
	stepCtx, cancel := context.WithDeadline(ctx, stepDeadline)
	defer cancel()
	return executeCommand(stepCtx, b.policies.command(stage), input, input.Worktree+"-runtime")
}

func (b *legacyDeliveryBackend) commit(ctx context.Context, workspace, base string, deadline time.Time) (string, error) {
	return b.repository(deadline).commit(ctx, workspace, base)
}

func (b *legacyDeliveryBackend) exists(ctx context.Context, sha string, deadline time.Time) error {
	return b.repository(deadline).exists(ctx, sha)
}

func (b *legacyDeliveryBackend) promote(ctx context.Context, base, candidate string, deadline time.Time) error {
	return b.repository(deadline).promote(ctx, b.policies.Spec.TargetBranch, base, candidate)
}

// Recovery observations keep the legacy finite step timeout independently of
// the elapsed work deadline. They never mutate the work repository's authority.
func (b *legacyDeliveryBackend) observeTarget(ctx context.Context) (string, error) {
	return b.repository(time.Time{}).resolveTarget(ctx, b.policies.Spec.TargetBranch)
}

func (b *legacyDeliveryBackend) completedArtifact(ctx context.Context, sha string) error {
	return b.repository(time.Time{}).exists(ctx, sha)
}
