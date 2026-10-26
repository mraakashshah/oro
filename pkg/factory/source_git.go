package factory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// managedGitRepository borrows the controller's journal and runtime. It never
// falls back to the legacy process adapter, including for observation.
type managedGitRepository struct {
	mu         sync.Mutex
	failed     error
	manifest   managedManifest
	generation uint64
	journal    *managedJournal
	host       HostRuntime
	limits     HostRetentionLimits
	source     sourceLimits
}

func newManagedGitRepository(manifest managedManifest, generation uint64, journal *managedJournal, host HostRuntime) (*managedGitRepository, error) {
	if journal == nil || host == nil || generation == 0 || !reflect.DeepEqual(managedJournalIdentity(manifest), journal.admission) {
		return nil, errors.New("managed Git requires its exact admitted journal, generation and runtime")
	}
	if err := validateManagedManifest(manifest.Reservation.StateRoot, manifest.Spec.ID, manifest); err != nil {
		return nil, err
	}
	limits, err := ManagedHostRetention(manifest.Spec)
	if err != nil {
		return nil, err
	}
	if manifest.Spec.Runtime.HostRequiredBytes < limits.MinimumBytes {
		return nil, errors.New("managed Git host reservation is below its complete retention bound")
	}
	return &managedGitRepository{
		manifest: manifest, generation: generation, journal: journal, host: host, limits: limits,
		source: sourceLimits{
			MaxArchiveBytes: limits.ArchiveBytes, MaxBytes: manifest.Spec.Runtime.SourceBytes, MaxFileBytes: manifest.Spec.Runtime.SourceBytes,
			MaxFiles: manifest.Spec.Runtime.MaxFiles, MaxPathBytes: limits.MaxPathBytes,
		},
	}, nil
}

// One complete operation shares a deadline; additional blobs cannot refresh it.
func (g *managedGitRepository) operation(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(time.Duration(g.manifest.Spec.StepTimeoutSeconds) * time.Second)
	if g.manifest.Deadline.Before(deadline) {
		deadline = g.manifest.Deadline
	}
	return context.WithDeadline(ctx, deadline)
}

func (g *managedGitRepository) invoke(ctx context.Context, input []byte, args ...string) (HostResult, error) {
	if len(args) == 0 {
		return HostResult{}, errors.New("managed Git command is empty")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed != nil {
		return HostResult{}, fmt.Errorf("managed Git requires scope reconciliation after runtime failure: %w", g.failed)
	}
	if err := ctx.Err(); err != nil {
		return HostResult{}, fmt.Errorf("managed Git canceled: %w", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.After(g.manifest.Deadline) || int64(len(input)) > g.limits.ArchiveBytes {
		return HostResult{}, errors.New("managed Git exceeds its frozen deadline or input allowance")
	}
	path := filepath.Join(g.manifest.Reservation.StateRoot, g.manifest.Spec.ID, "git")
	if err := verifyManagedArtifact(ctx, path, g.manifest.Programs["git"], 0o500); err != nil {
		return HostResult{}, err
	}
	argv := append([]string{
		"--no-replace-objects", "--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "commit.gpgSign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "i18n.commitEncoding=UTF-8",
	}, args...)
	call := HostInvocation{
		Path: path, Args: argv, Env: managedGitEnvironment(g.manifest.CreatedAt), Dir: g.manifest.Spec.Repository,
		Stdin: input, Deadline: deadline, OutputLimit: g.limits.HostOutputBytes,
	}
	if err := validateManagedHostRequest(call, g.limits); err != nil {
		return HostResult{}, err
	}
	scope, err := reserveManagedHostInvocation(ctx, g.journal, g.generation, deadline)
	if err != nil {
		return HostResult{}, err
	}
	call.Scope = scope
	result, err := g.host.Invoke(ctx, call)
	if err != nil {
		g.failed = err
		return HostResult{}, fmt.Errorf("managed Git %s: %w", args[0], err)
	}
	if int64(len(result.Stdout)) > call.OutputLimit || int64(len(result.Stderr)) > call.OutputLimit {
		return HostResult{}, errors.New("managed Git runtime exceeded its output allowance")
	}
	if err := ctx.Err(); err != nil {
		return HostResult{}, fmt.Errorf("managed Git result arrived after operation deadline: %w", err)
	}
	return result, nil
}

func (g *managedGitRepository) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("managed Git command is empty")
	}
	result, err := g.invoke(ctx, input, args...)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("managed Git %s exited with status %d", args[0], result.ExitCode)
	}
	return result.Stdout, nil
}

func managedGitEnvironment(created time.Time) []string {
	date := "@" + strconv.FormatInt(created.Unix(), 10) + " +0000"
	return []string{
		"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C", "LANG=C",
		"GIT_AUTHOR_NAME=Oro Factory", "GIT_AUTHOR_EMAIL=factory@oro.invalid", "GIT_COMMITTER_NAME=Oro Factory", "GIT_COMMITTER_EMAIL=factory@oro.invalid",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date,
	}
}

func (g *managedGitRepository) target(ctx context.Context) (string, error) {
	ctx, cancel := g.operation(ctx)
	defer cancel()
	return g.targetWithin(ctx)
}

func (g *managedGitRepository) targetWithin(ctx context.Context) (string, error) {
	ref := "refs/heads/" + g.manifest.Spec.TargetBranch
	out, err := g.run(ctx, nil, "show-ref", "--verify", "--hash", ref)
	if err != nil {
		return "", err
	}
	oid, err := parseSourceOIDLine(out)
	if err != nil {
		return "", err
	}
	if _, err := g.readCommit(ctx, oid); err != nil {
		return "", err
	}
	worktrees, err := g.run(ctx, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	for _, field := range strings.Split(string(worktrees), "\x00") {
		if field == "branch "+ref {
			return "", &targetCheckedOutError{branch: g.manifest.Spec.TargetBranch}
		}
	}
	return oid, nil
}

func (g *managedGitRepository) promote(ctx context.Context, base, candidate string) error {
	if !sourceOID(base) || !sourceOID(candidate) || len(base) != len(candidate) {
		return errors.New("managed promotion requires exact commit object IDs")
	}
	ctx, cancel := g.operation(ctx)
	defer cancel()
	if _, err := g.targetWithin(ctx); err != nil {
		return err
	}
	if _, err := g.readCommit(ctx, base); err != nil {
		return err
	}
	commit, err := g.readCommit(ctx, candidate)
	if err != nil {
		return err
	}
	if candidate != base && (len(commit.parents) != 1 || commit.parents[0] != base) {
		return errors.New("candidate does not have the exact approved base as its only parent")
	}
	_, err = g.run(ctx, nil, "update-ref", "--no-deref", "refs/heads/"+g.manifest.Spec.TargetBranch, candidate, base)
	return err
}
