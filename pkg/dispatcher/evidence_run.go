package dispatcher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/processenv"
)

const (
	maxEvidenceArgvBytes      = 4 * 1024
	maxEvidenceOutputSize     = 32 * 1024
	defaultEvidenceRunTimeout = 2 * time.Minute
	maxEvidenceRunTimeout     = 10 * time.Minute
)

// EvidenceRunStatus records the terminal result of a dispatcher-owned command.
type EvidenceRunStatus string

const (
	// EvidenceRunCompleted records a command that exited normally or non-zero.
	EvidenceRunCompleted EvidenceRunStatus = "completed"
	// EvidenceRunTimedOut records a command terminated by its execution deadline.
	EvidenceRunTimedOut EvidenceRunStatus = "timed_out"
	// EvidenceRunCancelled records a command terminated by caller cancellation.
	EvidenceRunCancelled EvidenceRunStatus = "cancelled"
	// EvidenceRunCrashed records a command that could not start or complete.
	EvidenceRunCrashed EvidenceRunStatus = "crashed"
)

// EvidenceRunRequest identifies an active assignment and the exact argv to run.
// The worktree and project are always resolved by the dispatcher, never the caller.
type EvidenceRunRequest struct {
	AssignmentID int64
	WorkerID     string
	BeadID       string
	Argv         []string
	Timeout      time.Duration
}

// EvidenceManifest is the durable identity and result of a diagnostic command.
type EvidenceManifest struct {
	ID           string
	Project      string
	AssignmentID int64
	WorkerID     string
	BeadID       string
	Worktree     string
	Branch       string
	HEAD         string
	Argv         []string
	StartedAt    time.Time
	CompletedAt  time.Time
	ExitCode     int
	Output       string
	Truncated    bool
	Status       EvidenceRunStatus
}

// RunEvidence executes req.Argv in the active assignment's stored worktree.
// It persists only terminal manifests, so an interrupted process can never be
// mistaken for usable evidence.
//
//oro:testonly — protocol request routing wires this dispatcher boundary in a later task.
func (d *Dispatcher) RunEvidence(ctx context.Context, req EvidenceRunRequest) (EvidenceManifest, error) {
	if d == nil || d.db == nil {
		return EvidenceManifest{}, errors.New("run evidence: dispatcher database is unavailable")
	}
	if err := validateEvidenceRunRequest(req); err != nil {
		return EvidenceManifest{}, err
	}

	worktree, err := d.evidenceAssignmentWorktree(ctx, req)
	if err != nil {
		return EvidenceManifest{}, err
	}
	branch, head, err := evidenceGitIdentity(ctx, worktree)
	if err != nil {
		return EvidenceManifest{}, err
	}

	manifest := EvidenceManifest{
		ID:           newEvidenceRunID(),
		Project:      evidenceProjectName(d.repoRoot),
		AssignmentID: req.AssignmentID,
		WorkerID:     req.WorkerID,
		BeadID:       req.BeadID,
		Worktree:     worktree,
		Branch:       branch,
		HEAD:         head,
		Argv:         append([]string(nil), req.Argv...),
		StartedAt:    time.Now().UTC(),
	}

	runCtx, cancel := context.WithTimeout(ctx, evidenceRunTimeout(req.Timeout))
	defer cancel()
	// #nosec G204 -- validateEvidenceRunRequest bounds argv; this API intentionally executes dispatcher-approved argv.
	command := exec.CommandContext(runCtx, req.Argv[0], req.Argv[1:]...)
	command.Dir = worktree
	command.Env = processenv.ForWorkdir(os.Environ(), worktree)
	output, runErr := command.CombinedOutput()
	manifest.Output, manifest.Truncated = truncateEvidenceOutput(output)
	manifest.ExitCode = evidenceExitCode(runErr)
	manifest.CompletedAt = time.Now().UTC()
	manifest.Status = evidenceRunStatus(runCtx, runErr)

	if err := d.persistEvidenceManifest(context.WithoutCancel(ctx), manifest); err != nil {
		return manifest, err
	}
	if manifest.Status == EvidenceRunCompleted {
		return manifest, nil
	}
	return manifest, fmt.Errorf("run evidence: %s", manifest.Status)
}

func evidenceRunTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultEvidenceRunTimeout
	}
	if timeout < maxEvidenceRunTimeout {
		return timeout
	}
	return maxEvidenceRunTimeout
}

func validateEvidenceRunRequest(req EvidenceRunRequest) error {
	if req.AssignmentID <= 0 || strings.TrimSpace(req.WorkerID) == "" || strings.TrimSpace(req.BeadID) == "" || len(req.Argv) == 0 || strings.TrimSpace(req.Argv[0]) == "" {
		return errors.New("run evidence: incomplete request")
	}
	if evidenceArgvSize(req.Argv) > maxEvidenceArgvBytes {
		return fmt.Errorf("run evidence: argv exceeds %d bytes", maxEvidenceArgvBytes)
	}
	return nil
}

func evidenceArgvSize(argv []string) int {
	size := 0
	for _, arg := range argv {
		size += len(arg)
	}
	return size
}

func (d *Dispatcher) evidenceAssignmentWorktree(ctx context.Context, req EvidenceRunRequest) (string, error) {
	var worktree string
	err := d.db.QueryRowContext(ctx, `
SELECT worktree FROM assignments
WHERE id=? AND worker_id=? AND bead_id=? AND status='active'`,
		req.AssignmentID, req.WorkerID, req.BeadID).Scan(&worktree)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("run evidence: active assignment not found")
	}
	if err != nil {
		return "", fmt.Errorf("run evidence: load assignment: %w", err)
	}
	if strings.TrimSpace(worktree) == "" {
		return "", errors.New("run evidence: assignment worktree is empty")
	}
	return worktree, nil
}

func evidenceGitIdentity(ctx context.Context, worktree string) (branch, head string, err error) {
	branch, err = evidenceGit(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return "", "", err
	}
	head, err = evidenceGit(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return branch, head, nil
}

func evidenceGit(ctx context.Context, worktree string, args ...string) (string, error) {
	// #nosec G204 -- callers pass fixed git identity arguments only.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run evidence git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func evidenceProjectName(repoRoot string) string {
	project := filepath.Base(filepath.Clean(repoRoot))
	if project == "." || project == string(filepath.Separator) {
		return ""
	}
	return project
}

func truncateEvidenceOutput(output []byte) (string, bool) {
	if len(output) <= maxEvidenceOutputSize {
		return string(output), false
	}
	return string(output[:maxEvidenceOutputSize]), true
}

func evidenceExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}

func evidenceRunStatus(ctx context.Context, err error) EvidenceRunStatus {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return EvidenceRunTimedOut
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return EvidenceRunCancelled
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return EvidenceRunCompleted
	}
	if err != nil {
		return EvidenceRunCrashed
	}
	return EvidenceRunCompleted
}

func (d *Dispatcher) persistEvidenceManifest(ctx context.Context, manifest EvidenceManifest) error {
	argvJSON, err := json.Marshal(manifest.Argv)
	if err != nil {
		return fmt.Errorf("run evidence: encode argv: %w", err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("run evidence: encode manifest: %w", err)
	}
	hash := sha256.Sum256(manifestJSON)
	_, err = d.db.ExecContext(ctx, `
INSERT INTO evidence_runs (id, assignment_id, worker_id, bead_id, kind, argv_json, manifest_hash, exit_code, output, status, started_at, completed_at)
VALUES (?, ?, ?, ?, 'diagnostic', ?, ?, ?, ?, ?, ?, ?)`,
		manifest.ID, manifest.AssignmentID, manifest.WorkerID, manifest.BeadID, string(argvJSON), hex.EncodeToString(hash[:]), manifest.ExitCode,
		manifest.Output, string(manifest.Status), manifest.StartedAt.Format(time.RFC3339Nano), manifest.CompletedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("run evidence: persist manifest: %w", err)
	}
	return nil
}

func newEvidenceRunID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("evidence-%d", time.Now().UTC().UnixNano())
	}
	return "evidence-" + hex.EncodeToString(random[:])
}
