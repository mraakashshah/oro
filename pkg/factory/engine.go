package factory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

// Run drives one admitted objective to durable completion or bounded failure.
// It requires trusted adapters or external isolation; it does not sandbox code.
func Run(ctx context.Context, stateDir string, spec Spec) (Status, error) {
	return run(ctx, stateDir, spec, runHooks{})
}

// ReadStatus reads existing state without creating or advancing an objective.
func ReadStatus(ctx context.Context, stateDir, id string) (Status, error) {
	if !filepath.IsAbs(stateDir) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(id) {
		return Status{}, errors.New("absolute state directory and valid objective ID are required")
	}
	s, err := readSnapshot(ctx, filepath.Join(stateDir, id))
	if err != nil {
		return Status{}, err
	}
	if s.Spec.ID != id {
		return Status{}, errors.New("stored objective identity mismatch")
	}
	return snapshotStatus(s), nil
}

type runHooks struct {
	afterTransition func(snapshot) error
	afterPromotion  func(snapshot) error
}

type controller struct {
	store    *factoryStore
	policies policySet
	repo     gitRepository
	state    snapshot
	hooks    runHooks
}

func run(ctx context.Context, stateDir string, spec Spec, hooks runHooks) (Status, error) {
	if err := validateRepository(ctx, spec); err != nil {
		return Status{}, err
	}
	policies, err := loadPolicies(stateDir, spec)
	if err != nil {
		return Status{}, err
	}
	unlock, err := lockObjective(policies.Dir)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = unlock() }()
	store, err := openStore(ctx, policies.Dir)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = store.close() }()
	c := controller{
		store: store, policies: policies,
		repo: gitRepository{path: spec.Repository, stepTimeout: time.Duration(spec.StepTimeoutSeconds) * time.Second}, hooks: hooks,
	}
	if err := c.admit(ctx); err != nil {
		return snapshotStatus(c.state), err
	}
	if err := c.resume(ctx); err != nil {
		return snapshotStatus(c.state), err
	}
	c.repo.deadline = c.state.Deadline
	for c.state.State != "completed" && c.state.State != "failed" {
		if err := ctx.Err(); err != nil {
			return snapshotStatus(c.state), fmt.Errorf("factory interrupted: %w", err)
		}
		if err := c.step(ctx); err != nil {
			return snapshotStatus(c.state), errors.Join(err, ctx.Err())
		}
	}
	if c.state.State == "completed" {
		observer := gitRepository{path: c.repo.path, stepTimeout: c.repo.stepTimeout}
		if err := observer.exists(ctx, c.state.CandidateSHA); err != nil {
			return snapshotStatus(c.state), fmt.Errorf("completed artifact unavailable: %w", err)
		}
	}
	return snapshotStatus(c.state), nil
}

func (c *controller) admit(ctx context.Context) error {
	s, err := c.store.load(ctx)
	if err == nil {
		c.state = s
		if s.PolicyHash != c.policies.Hash || s.Spec.ID != c.policies.Spec.ID {
			return errors.New("objective specification or source policies changed since admission")
		}
		return verifyPolicies(c.policies)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := c.repo.target(ctx, c.policies.Spec.TargetBranch); err != nil {
		return err
	}
	if err := freezePolicies(c.policies); err != nil {
		return err
	}
	now := time.Now().UTC()
	return c.save(ctx, snapshot{
		Spec: c.policies.Spec, PolicyHash: c.policies.Hash, PolicyDigests: c.policies.Digests,
		CreatedAt: now, Deadline: now.Add(time.Duration(c.policies.Spec.TimeoutSeconds) * time.Second), State: "pending",
	})
}

func (c *controller) resume(ctx context.Context) error {
	switch c.state.State {
	case "pending", "approved", "completed", "failed":
		return nil
	case "running", "candidate", "accepted":
		return c.reject(ctx, errors.New("previous controller interrupted; prior attempt remains spent"))
	case "promoting":
		return c.reconcilePromotion(ctx)
	default:
		return fmt.Errorf("unknown durable factory state %q", c.state.State)
	}
}

func (c *controller) reconcilePromotion(ctx context.Context) error {
	observer := gitRepository{path: c.repo.path, stepTimeout: c.repo.stepTimeout}
	actual, err := observer.resolveTarget(ctx, c.state.Spec.TargetBranch)
	if err != nil {
		return err
	}
	if actual == c.state.CandidateSHA {
		// The authorized Git effect already happened; elapsed time cannot undo it.
		next := c.state
		next.State, next.Reason = "completed", ""
		return c.save(ctx, next)
	}
	if actual != c.state.BaseSHA {
		return c.reject(ctx, errors.New("target advanced during interrupted promotion"))
	}
	return nil
}

func (c *controller) step(ctx context.Context) error {
	switch c.state.State {
	case "pending":
		return c.attempt(ctx)
	case "candidate":
		return c.check(ctx, "acceptance")
	case "accepted":
		return c.check(ctx, "review")
	case "approved":
		if err := c.authorizePromotion(ctx); err != nil {
			return c.reject(ctx, err)
		}
		next := c.state
		next.State = "promoting"
		return c.save(ctx, next)
	case "promoting":
		return c.promote(ctx)
	default:
		return fmt.Errorf("cannot execute factory state %q", c.state.State)
	}
}

func (c *controller) attempt(ctx context.Context) error {
	next := reserveAttempt(c.state, time.Now())
	if err := c.save(ctx, next); err != nil || c.state.State == "failed" {
		return err
	}
	base, err := c.repo.target(ctx, c.state.Spec.TargetBranch)
	if err != nil {
		return c.fail(ctx, err)
	}
	path := filepath.Join(c.policies.Dir, fmt.Sprintf("attempt-%d", c.state.Attempts), "worker")
	if err := c.repo.checkout(ctx, path, base); err != nil {
		return c.reject(ctx, err)
	}
	next = c.state
	next.BaseSHA, next.Worktree = base, path
	if err := c.save(ctx, next); err != nil {
		return err
	}
	result := c.execute(ctx, "worker", path)
	if err := commandFailure(result); err != nil {
		return c.reject(ctx, err)
	}
	candidate, err := c.repo.commit(ctx, path, base)
	if err != nil {
		return c.reject(ctx, err)
	}
	next = c.state
	next.State, next.CandidateSHA = "candidate", candidate
	return c.save(ctx, next)
}

func (c *controller) check(ctx context.Context, stage string) error {
	path := filepath.Join(c.policies.Dir, fmt.Sprintf("attempt-%d", c.state.Attempts), stage)
	if err := c.repo.checkout(ctx, path, c.state.CandidateSHA); err != nil {
		return c.reject(ctx, err)
	}
	if err := c.repo.pristine(ctx, path, c.state.CandidateSHA); err != nil {
		return c.reject(ctx, err)
	}
	result := c.execute(ctx, stage, path)
	if err := c.repo.pristine(ctx, path, c.state.CandidateSHA); err != nil {
		return c.reject(ctx, err)
	}
	next := c.state
	var err error
	if stage == "acceptance" {
		next.Acceptance, err = acceptEvidence(result, next.CandidateSHA, next.BaseSHA, next.PolicyHash, next.Spec.RequiredChecks)
		next.State = "accepted"
	} else {
		next.Review, err = reviewEvidence(result, next.CandidateSHA, next.BaseSHA, next.PolicyHash)
		next.State = "approved"
	}
	if err != nil {
		return c.reject(ctx, err)
	}
	return c.save(ctx, next)
}

func (c *controller) execute(ctx context.Context, stage, path string) commandResult {
	if err := verifyPolicies(c.policies); err != nil {
		return commandResult{ExitCode: -1, Err: err}
	}
	deadline := time.Now().Add(time.Duration(c.state.Spec.StepTimeoutSeconds) * time.Second)
	if c.state.Deadline.Before(deadline) {
		deadline = c.state.Deadline
	}
	stepCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	input := commandInput{
		Objective: c.state.Spec.Objective, Attempt: c.state.Attempts, Worktree: path,
		CandidateSHA: c.state.CandidateSHA, BaseSHA: c.state.BaseSHA, Feedback: c.state.Feedback,
	}
	return executeCommand(stepCtx, c.policies.command(stage), input, path+"-runtime")
}

func (c *controller) authorizePromotion(ctx context.Context) error {
	if !time.Now().Before(c.state.Deadline) {
		return errors.New("objective deadline elapsed before promotion")
	}
	if err := verifyPolicies(c.policies); err != nil {
		return err
	}
	for stage, proof := range map[string]*evidence{"acceptance": c.state.Acceptance, "review": c.state.Review} {
		if proof == nil || proof.Stage != stage || proof.CandidateSHA != c.state.CandidateSHA ||
			proof.BaseSHA != c.state.BaseSHA || proof.PolicyHash != c.state.PolicyHash ||
			proof.ExitCode != 0 || len(proof.OutputHash) != 64 {
			return fmt.Errorf("invalid %s evidence for promotion", stage)
		}
	}
	return c.repo.exists(ctx, c.state.CandidateSHA)
}

func (c *controller) promote(ctx context.Context) error {
	if err := c.authorizePromotion(ctx); err != nil {
		return c.reject(ctx, err)
	}
	if err := c.repo.promote(ctx, c.state.Spec.TargetBranch, c.state.BaseSHA, c.state.CandidateSHA); err != nil {
		var checkedOut *targetCheckedOutError
		if errors.As(err, &checkedOut) {
			return c.fail(ctx, err)
		}
		return c.reject(ctx, err)
	}
	if c.hooks.afterPromotion != nil {
		if err := c.hooks.afterPromotion(c.state); err != nil {
			return err
		}
	}
	// Read proof of the authorized effect even if its deadline just elapsed.
	observer := gitRepository{path: c.repo.path, stepTimeout: c.repo.stepTimeout}
	actual, err := observer.resolveTarget(ctx, c.state.Spec.TargetBranch)
	if err != nil {
		return err
	}
	if actual != c.state.CandidateSHA {
		return errors.New("target advanced before completion could be recorded")
	}
	next := c.state
	next.State, next.Reason = "completed", ""
	return c.save(ctx, next)
}

func (c *controller) reject(ctx context.Context, reason error) error {
	next := c.state
	next.State, next.Feedback = "pending", boundedReason(reason)
	next.BaseSHA, next.CandidateSHA, next.Worktree = "", "", ""
	next.Acceptance, next.Review = nil, nil
	return c.save(ctx, next)
}

func (c *controller) fail(ctx context.Context, reason error) error {
	next := c.state
	next.State, next.Reason = "failed", boundedReason(reason)
	return c.save(ctx, next)
}

func (c *controller) save(ctx context.Context, next snapshot) error {
	if err := c.store.save(ctx, next, c.state.Revision); err != nil {
		return err
	}
	next.Revision = c.state.Revision + 1
	c.state = next
	if c.hooks.afterTransition != nil {
		return c.hooks.afterTransition(next)
	}
	return nil
}

func commandFailure(result commandResult) error {
	if result.Err == nil && result.ExitCode == 0 {
		return nil
	}
	return errors.Join(result.Err, fmt.Errorf("policy exited %d; stdout=%s; stderr=%s", result.ExitCode, result.Stdout, result.Stderr))
}

func boundedReason(err error) string {
	const maxReason = 16 << 10
	text := err.Error()
	if len(text) > maxReason {
		return text[:maxReason] + " [diagnostics truncated]"
	}
	return text
}

func snapshotStatus(s snapshot) Status {
	return Status{
		ID: s.Spec.ID, State: s.State, Attempts: s.Attempts, CandidateSHA: s.CandidateSHA,
		TargetBranch: s.Spec.TargetBranch, Reason: s.Reason,
	}
}
