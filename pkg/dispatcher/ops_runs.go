package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"oro/pkg/agentmodel"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	opsRunStatusRunning    = "running"
	opsRunStatusFailed     = "failed"
	opsRunStatusStale      = "stale"
	opsRunStatusResolved   = "resolved"
	opsRunStatusSuperseded = "superseded"
)

// OpsRunRecord is the dispatcher-owned durable representation of an ops agent
// lifecycle row.
type OpsRunRecord struct {
	ID            int64
	EscalationID  int64
	Type          string
	BeadID        string
	WorkerID      string
	DispatcherPID int
	ProcessPID    int
	Runtime       string
	Model         string
	Status        string
	Verdict       string
	Feedback      string
	Error         string
	StartedAt     string
	CompletedAt   string
}

// CreateOpsRun inserts an ops run unless an existing blocking run already owns
// the same type/bead key. The returned bool is true only for a newly inserted
// row.
func CreateOpsRun(ctx context.Context, db *sql.DB, rec OpsRunRecord) (OpsRunRecord, bool, error) {
	if db == nil {
		return OpsRunRecord{}, false, errors.New("create ops run: db is nil")
	}
	return createOpsRun(ctx, db, rec)
}

type opsRunStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func createOpsRun(ctx context.Context, store opsRunStore, rec OpsRunRecord) (OpsRunRecord, bool, error) {
	rec = normalizeOpsRunRecord(rec)
	if err := validateOpsRunStatus(rec.Status); err != nil {
		return OpsRunRecord{}, false, err
	}

	result, err := store.ExecContext(ctx, `
INSERT INTO ops_runs (
  escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid,
  runtime, model, status, verdict, feedback, error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableInt64(rec.EscalationID), rec.Type, rec.BeadID, rec.WorkerID,
		nullableInt(rec.DispatcherPID), nullableInt(rec.ProcessPID),
		rec.Runtime, rec.Model, rec.Status, rec.Verdict, rec.Feedback, rec.Error)
	if err != nil {
		if !isSQLiteUniqueConstraint(err) {
			return OpsRunRecord{}, false, fmt.Errorf("create ops run: %w", err)
		}
		blocking, findErr := findBlockingOpsRun(ctx, store, rec.Type, rec.BeadID)
		if findErr == nil && blocking != nil {
			return *blocking, false, nil
		}
		return OpsRunRecord{}, false, fmt.Errorf("create ops run: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return OpsRunRecord{}, false, fmt.Errorf("create ops run id: %w", err)
	}
	created, err := loadOpsRunByID(ctx, store, id)
	if err != nil {
		return OpsRunRecord{}, false, err
	}
	return created, true, nil
}

func isSQLiteUniqueConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// FindBlockingOpsRun returns the active blocking row for type/bead, if any.
// Resolved and superseded rows intentionally do not block a fresh run.
func FindBlockingOpsRun(ctx context.Context, db *sql.DB, runType, beadID string) (*OpsRunRecord, error) {
	if db == nil {
		return nil, errors.New("find blocking ops run: db is nil")
	}
	return findBlockingOpsRun(ctx, db, runType, beadID)
}

func findBlockingOpsRun(ctx context.Context, store opsRunStore, runType, beadID string) (*OpsRunRecord, error) {
	rec, err := scanOpsRun(store.QueryRowContext(ctx, `
SELECT id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at
FROM ops_runs
WHERE type = ?
  AND bead_id = ?
  AND status IN ('running', 'failed', 'stale')
ORDER BY id DESC
LIMIT 1`, runType, beadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find blocking ops run: %w", err)
	}
	return &rec, nil
}

// CompleteOpsRun records the terminal outcome of an ops run. Failed and stale
// outcomes remain blocking until a later operator or dispatcher action resolves
// or supersedes them.
func CompleteOpsRun(ctx context.Context, db *sql.DB, id int64, status, verdict, feedback, errorText string) error {
	if db == nil {
		return errors.New("complete ops run: db is nil")
	}
	_, err := completeOpsRunFromStatus(ctx, db, id, opsRunStatusRunning, status, verdict, feedback, errorText)
	return err
}

type opsRunCompletionOutcome uint8

const (
	opsRunCompletionAcquired opsRunCompletionOutcome = iota
	opsRunCompletionExactReplay
)

func completeOpsRunFromStatus(
	ctx context.Context,
	store opsRunStore,
	id int64,
	expectedStatus, status, verdict, feedback, errorText string,
) (opsRunCompletionOutcome, error) {
	if !isBlockingOpsRunStatus(expectedStatus) {
		return 0, fmt.Errorf("complete ops run %d: invalid expected status %q", id, expectedStatus)
	}
	if err := validateOpsRunCompletionStatus(status); err != nil {
		return 0, err
	}
	result, err := store.ExecContext(ctx, `
UPDATE ops_runs
SET status = ?,
    verdict = ?,
    feedback = ?,
    error = ?,
    completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
WHERE id = ?
  AND status = ?`, status, verdict, feedback, errorText, id, expectedStatus)
	if err != nil {
		return 0, fmt.Errorf("complete ops run %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("complete ops run %d rows affected: %w", id, err)
	}
	if affected == 0 {
		current, loadErr := loadOpsRunByID(ctx, store, id)
		if errors.Is(loadErr, sql.ErrNoRows) {
			return 0, fmt.Errorf("complete ops run %d: not found", id)
		}
		if loadErr != nil {
			return 0, loadErr
		}
		if current.Status == status && current.Verdict == verdict && current.Feedback == feedback && current.Error == errorText {
			return opsRunCompletionExactReplay, nil
		}
		return 0, fmt.Errorf("complete ops run %d: expected status %q, found %q", id, expectedStatus, current.Status)
	}
	return opsRunCompletionAcquired, nil
}

func (d *Dispatcher) applyOpsDirective(dir protocol.Directive, args string) (detail string, handled bool, err error) {
	switch dir {
	case protocol.DirectiveOpsRuns:
		detail, err := d.applyOpsRuns()
		return detail, true, err
	case protocol.DirectiveOpsRetry:
		detail, err := d.applyOpsRetry(args)
		return detail, true, err
	case protocol.DirectiveOpsResolve:
		detail, err := d.applyOpsResolve(args)
		return detail, true, err
	default:
		return "", false, nil
	}
}

func (d *Dispatcher) applyOpsRuns() (string, error) {
	if d == nil || d.db == nil {
		return "", errors.New("ops-runs: dispatcher db is nil")
	}
	rows, err := d.db.QueryContext(context.Background(), `
SELECT id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at
FROM ops_runs
ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("query ops runs: %w", err)
	}
	defer rows.Close()

	runs := make([]opsRunJSON, 0)
	for rows.Next() {
		rec, scanErr := scanOpsRun(rows)
		if scanErr != nil {
			return "", scanErr
		}
		runs = append(runs, newOpsRunJSON(rec))
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate ops runs: %w", err)
	}

	b, err := json.Marshal(runs)
	if err != nil {
		return "", fmt.Errorf("marshal ops runs: %w", err)
	}
	return string(b), nil
}

func (d *Dispatcher) applyOpsRetry(args string) (string, error) {
	runID, err := parseOpsRunIDArg(args, "ops-retry")
	if err != nil {
		return "", err
	}
	rec, err := loadOpsRunByID(context.Background(), d.db, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("ops run %d not found", runID)
	}
	if err != nil {
		return "", err
	}

	resp := opsRetryResponse{
		ID:     rec.ID,
		Status: rec.Status,
	}
	if !isBlockingOpsRunStatus(rec.Status) {
		b, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			return "", fmt.Errorf("marshal ops retry response: %w", marshalErr)
		}
		return string(b), nil
	}

	created, routed, err := d.supersedeOpsRunForRetry(rec)
	if err != nil {
		return "", err
	}
	resp.Retried = true
	resp.Status = opsRunStatusSuperseded
	resp.NewOpsRunID = created.ID
	resp.Routed = routed

	b, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("marshal ops retry response: %w", err)
	}
	return string(b), nil
}

func (d *Dispatcher) supersedeOpsRunForRetry(rec OpsRunRecord) (OpsRunRecord, bool, error) {
	if !isBlockingOpsRunStatus(rec.Status) {
		blocking, err := FindBlockingOpsRun(context.Background(), d.db, rec.Type, rec.BeadID)
		if err != nil {
			return OpsRunRecord{}, false, err
		}
		if blocking != nil {
			return OpsRunRecord{}, false, fmt.Errorf("retry ops run %d: blocking ops run %d already exists", rec.ID, blocking.ID)
		}
		return OpsRunRecord{}, false, fmt.Errorf("retry ops run %d: status %q is not retryable", rec.ID, rec.Status)
	}
	next := rec
	next.ID = 0
	next.Status = opsRunStatusRunning
	next.DispatcherPID = os.Getpid()
	next.ProcessPID = 0
	next.Verdict = ""
	next.Feedback = ""
	next.Error = fmt.Sprintf("manual retry of ops run %d", rec.ID)
	if ops.Type(rec.Type) == ops.OpsDecompose || ops.Type(rec.Type) == ops.OpsEscalation {
		next.Error = rec.Error
	}
	next.StartedAt = ""
	next.CompletedAt = ""
	fillOpsRunRuntimeModel(&next)

	created, wasCreated, err := replaceOpsRun(
		context.Background(), d.db, rec, next, fmt.Sprintf("manual retry superseded ops run %d", rec.ID),
	)
	if err != nil {
		return OpsRunRecord{}, false, err
	}
	if !wasCreated {
		return OpsRunRecord{}, false, fmt.Errorf("retry ops run %d: blocking ops run %d already exists", rec.ID, created.ID)
	}

	d.clearAssignmentFailure(rec.BeadID)
	return created, d.routeOpsRun(context.Background(), created), nil
}

func (d *Dispatcher) applyOpsResolve(args string) (string, error) {
	runID, reason, err := parseOpsResolveArgs(args)
	if err != nil {
		return "", err
	}
	rec, err := loadOpsRunByID(context.Background(), d.db, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("ops run %d not found", runID)
	}
	if err != nil {
		return "", err
	}

	resp := opsResolveResponse{
		ID:     rec.ID,
		Status: rec.Status,
		Reason: reason,
	}
	if rec.Status == opsRunStatusResolved {
		resp.Resolved = true
		b, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			return "", fmt.Errorf("marshal ops resolve response: %w", marshalErr)
		}
		return string(b), nil
	}

	if ops.Type(rec.Type) == ops.OpsDecompose {
		if err := d.validateDecomposeResult(context.Background(), rec.BeadID); err != nil {
			return "", err
		}
	}
	if _, err := completeOpsRunFromStatus(
		context.Background(), d.db, rec.ID, rec.Status, opsRunStatusResolved, rec.Verdict, rec.Feedback, reason,
	); err != nil {
		return "", err
	}
	d.ackEscalation(context.Background(), rec.EscalationID, rec.BeadID, rec.WorkerID)
	d.clearAssignmentFailure(rec.BeadID)

	resp.Resolved = true
	resp.Status = opsRunStatusResolved
	b, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("marshal ops resolve response: %w", err)
	}
	return string(b), nil
}

type opsRunJSON struct {
	ID            int64  `json:"id"`
	EscalationID  int64  `json:"escalation_id,omitempty"`
	Type          string `json:"type"`
	BeadID        string `json:"bead_id,omitempty"`
	WorkerID      string `json:"worker_id,omitempty"`
	DispatcherPID int    `json:"dispatcher_pid,omitempty"`
	ProcessPID    int    `json:"process_pid,omitempty"`
	Runtime       string `json:"runtime,omitempty"`
	Model         string `json:"model,omitempty"`
	Status        string `json:"status"`
	Verdict       string `json:"verdict,omitempty"`
	Feedback      string `json:"feedback,omitempty"`
	Error         string `json:"error,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
}

type opsRetryResponse struct {
	ID          int64  `json:"id"`
	Retried     bool   `json:"retried"`
	Status      string `json:"status"`
	NewOpsRunID int64  `json:"new_ops_run_id,omitempty"`
	Routed      bool   `json:"routed,omitempty"`
}

type opsResolveResponse struct {
	ID       int64  `json:"id"`
	Resolved bool   `json:"resolved"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

func newOpsRunJSON(rec OpsRunRecord) opsRunJSON {
	return opsRunJSON(rec)
}

func parseOpsRunIDArg(args, directive string) (int64, error) {
	id := strings.TrimSpace(args)
	if id == "" {
		return 0, fmt.Errorf("%s requires an ops run ID", directive)
	}
	runID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || runID <= 0 {
		return 0, fmt.Errorf("%s requires a numeric ops run ID", directive)
	}
	return runID, nil
}

func parseOpsResolveArgs(args string) (runID int64, reason string, err error) {
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) < 2 {
		return 0, "", errors.New("ops-resolve requires an ops run ID and reason")
	}
	runID, err = parseOpsRunIDArg(fields[0], "ops-resolve")
	if err != nil {
		return 0, "", err
	}
	reason = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), fields[0]))
	if reason == "" {
		return 0, "", errors.New("ops-resolve requires a reason")
	}
	return runID, reason, nil
}

func isBlockingOpsRunStatus(status string) bool {
	switch status {
	case opsRunStatusRunning, opsRunStatusFailed, opsRunStatusStale:
		return true
	default:
		return false
	}
}

func (d *Dispatcher) reconcileOpsRunsOnStartup(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("reconcile ops runs: dispatcher db is nil")
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at
FROM ops_runs
WHERE status = 'running'
ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query running ops runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var running []OpsRunRecord
	for rows.Next() {
		rec, scanErr := scanOpsRun(rows)
		if scanErr != nil {
			return fmt.Errorf("scan running ops run: %w", scanErr)
		}
		running = append(running, rec)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate running ops runs: %w", err)
	}

	for _, rec := range running {
		if rec.DispatcherPID == os.Getpid() {
			continue
		}
		if isProcessAlive(rec.ProcessPID) {
			if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusStale, rec.Verdict, rec.Feedback, fmt.Sprintf("orphaned live process pid %d", rec.ProcessPID)); err != nil {
				return err
			}
			_ = d.logEvent(ctx, "ops_run_marked_stale", "dispatcher", rec.BeadID, rec.WorkerID,
				fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"process_pid":%d}`, rec.ID, rec.Type, rec.ProcessPID))
			continue
		}
		if err := d.supersedeAndRerouteOpsRun(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) supersedeAndRerouteOpsRun(ctx context.Context, rec OpsRunRecord) error {
	next := rec
	next.ID = 0
	next.Status = opsRunStatusRunning
	next.DispatcherPID = os.Getpid()
	next.ProcessPID = 0
	next.Verdict = ""
	next.Feedback = ""
	next.Error = ""
	next.StartedAt = ""
	next.CompletedAt = ""
	fillOpsRunRuntimeModel(&next)
	created, wasCreated, err := replaceOpsRun(
		ctx, d.db, rec, next, "orphaned dead process superseded on dispatcher startup",
	)
	if err != nil {
		return err
	}
	if !wasCreated {
		return nil
	}
	routed := d.routeOpsRun(ctx, created)
	if !routed {
		const diagnostic = "orphaned ops run replacement could not be routed on dispatcher startup"
		if err := CompleteOpsRun(ctx, d.db, created.ID, opsRunStatusFailed, "", "", diagnostic); err != nil {
			return fmt.Errorf("fail unroutable replacement ops run %d: %w", created.ID, err)
		}
	}
	_ = d.logEvent(ctx, "ops_run_superseded", "dispatcher", rec.BeadID, rec.WorkerID,
		fmt.Sprintf(`{"ops_run_id":%d,"new_ops_run_id":%d,"type":%q,"routed":%t}`, rec.ID, created.ID, rec.Type, routed))
	return nil
}

func replaceOpsRun(
	ctx context.Context,
	db *sql.DB,
	current OpsRunRecord,
	next OpsRunRecord,
	supersedeReason string,
) (OpsRunRecord, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return OpsRunRecord{}, false, fmt.Errorf("begin ops run replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	outcome, err := completeOpsRunFromStatus(
		ctx, tx, current.ID, current.Status, opsRunStatusSuperseded, current.Verdict, current.Feedback, supersedeReason,
	)
	if err != nil {
		return OpsRunRecord{}, false, err
	}
	if outcome != opsRunCompletionAcquired {
		return OpsRunRecord{}, false, fmt.Errorf("replace ops run %d: completion ownership was not acquired", current.ID)
	}
	created, wasCreated, err := createOpsRun(ctx, tx, next)
	if err != nil {
		return OpsRunRecord{}, false, err
	}
	if !wasCreated {
		return created, false, nil
	}
	if err := tx.Commit(); err != nil {
		return OpsRunRecord{}, false, fmt.Errorf("commit ops run replacement: %w", err)
	}
	return created, true, nil
}

func (d *Dispatcher) routeOpsRun(ctx context.Context, rec OpsRunRecord) bool {
	if d.ops == nil {
		return false
	}
	var resultCh <-chan ops.Result
	switch ops.Type(rec.Type) {
	case ops.OpsReview:
		return d.routeReviewOpsRun(ctx, rec)
	case ops.OpsDecompose:
		resultCh = d.ops.Decompose(ctx, ops.DecomposeOpts{
			BeadID:  rec.BeadID,
			Workdir: d.workdirForOpsRun(rec.BeadID),
			Reason:  rec.Error,
		})
	case ops.OpsWriteAC:
		title, description := d.beadContextForOpsRun(ctx, rec.BeadID)
		resultCh = d.ops.WriteAC(ctx, ops.WriteACOpts{
			BeadID:          rec.BeadID,
			BeadTitle:       title,
			BeadDescription: description,
			Workdir:         d.workdirForOpsRun(rec.BeadID),
		})
	case ops.OpsDiagnosis:
		resultCh = d.ops.Diagnose(ctx, ops.DiagOpts{
			BeadID:   rec.BeadID,
			Worktree: d.workdirForOpsRun(rec.BeadID),
			Symptom:  "orphaned ops run rerouted on dispatcher startup",
		})
	case ops.OpsEscalation:
		title, description := d.beadContextForOpsRun(ctx, rec.BeadID)
		escType := extractEscalationType(rec.Error)
		if escType == "" {
			escType = "ORPHANED_OPS_RUN"
		}
		resultCh = d.ops.Escalate(ctx, ops.EscalationOpts{
			EscalationType: escType,
			BeadID:         rec.BeadID,
			BeadTitle:      title,
			BeadContext:    description,
			RecentHistory:  rec.Error,
			Workdir:        d.workdirForOpsRun(rec.BeadID),
		})
	case ops.OpsDream:
		resultCh = d.ops.Dream(ctx, d.dreamOpts(ctx))
	default:
		return false
	}
	d.watchReroutedOpsRunResult(ctx, rec, resultCh, nil)
	return true
}

func (d *Dispatcher) routeReviewOpsRun(ctx context.Context, rec OpsRunRecord) bool {
	if err := d.observeStorageController(ctx); err != nil || !d.storageAdmissionAllowed() {
		return false
	}
	reviewCtx := d.reviewContextForOpsRun(ctx, rec)
	if reviewCtx.worktree == "" || reviewCtx.targetBranch == "" {
		return false
	}
	title, acceptance, _ := d.lookupBeadDetail(ctx, rec.BeadID, rec.WorkerID)
	resultCh := d.ops.Review(ctx, ops.ReviewOpts{
		BeadID:             rec.BeadID,
		BeadTitle:          title,
		Worktree:           reviewCtx.worktree,
		AcceptanceCriteria: acceptance,
		BaseBranch:         reviewCtx.targetBranch,
		ProjectRoot:        reviewCtx.worktree,
	})
	d.watchReroutedOpsRunResult(ctx, rec, resultCh, func(result ops.Result) {
		forward := make(chan ops.Result, 1)
		forward <- result
		d.handleReviewResultForAssignment(ctx, reviewCtx.workerID, rec.BeadID, reviewCtx.assignmentID, forward)
	})
	return true
}

func (d *Dispatcher) watchReroutedOpsRunResult(
	ctx context.Context,
	rec OpsRunRecord,
	resultCh <-chan ops.Result,
	afterComplete func(ops.Result),
) {
	d.safeGo(func() {
		result := <-resultCh
		status, verdict, errorText := terminalOpsRunResult(result)
		if rec.ID <= 0 {
			return
		}
		outcome, err := completeOpsRunFromStatus(
			ctx, d.db, rec.ID, opsRunStatusRunning, status, verdict, result.Feedback, errorText,
		)
		if err != nil {
			_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", rec.BeadID, rec.WorkerID,
				fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"status":%q,"error":%q}`, rec.ID, rec.Type, status, err.Error()))
			return
		}
		if outcome != opsRunCompletionAcquired {
			return
		}
		if afterComplete != nil {
			afterComplete(result)
		}
	})
}

func terminalOpsRunResult(result ops.Result) (status, verdict, errorText string) {
	status = opsRunStatusResolved
	verdict = string(result.Verdict)
	if result.Err != nil {
		status = opsRunStatusFailed
		errorText = result.Err.Error()
	} else if result.Verdict == ops.VerdictFailed {
		status = opsRunStatusFailed
		errorText = result.Feedback
	}
	if verdict != "" {
		return status, verdict, errorText
	}
	if status == opsRunStatusFailed {
		verdict = string(ops.VerdictFailed)
	} else {
		verdict = string(ops.VerdictResolved)
	}
	return status, verdict, errorText
}

type reviewOpsRunContext struct {
	worktree     string
	targetBranch string
	workerID     string
	assignmentID int64
}

func (d *Dispatcher) reviewContextForOpsRun(ctx context.Context, rec OpsRunRecord) reviewOpsRunContext {
	if d == nil || rec.BeadID == "" {
		return reviewOpsRunContext{}
	}
	checkpoint, err := NewReviewCheckpointStore(d.db).LoadOwningForBead(ctx, rec.BeadID)
	if err != nil {
		_ = d.logEvent(ctx, "review_checkpoint_context_restore_failed", "dispatcher", rec.BeadID, rec.WorkerID, err.Error())
		return reviewOpsRunContext{}
	}
	if checkpoint != nil {
		d.mu.Lock()
		d.worktreeByBead[rec.BeadID] = checkpoint.Worktree
		d.mu.Unlock()
		assignmentID := checkpoint.CurrentAssignmentID
		if assignmentID <= 0 {
			assignmentID = checkpoint.OriginAssignmentID
		}
		return reviewOpsRunContext{
			worktree:     checkpoint.Worktree,
			targetBranch: checkpoint.TargetBranch,
			workerID:     firstNonEmpty(checkpoint.WorkerID, rec.WorkerID),
			assignmentID: assignmentID,
		}
	}
	d.mu.Lock()
	reviewCtx := d.reviewContextFromWorkerLocked(rec)
	if reviewCtx.worktree == "" {
		reviewCtx.worktree = d.worktreeByBead[rec.BeadID]
	}
	d.mu.Unlock()

	if reviewCtx.worktree == "" {
		return reviewOpsRunContext{}
	}
	if reviewCtx.targetBranch == "" {
		reviewCtx.targetBranch = d.cfg.DefaultBranch
	}
	return reviewCtx
}

func (d *Dispatcher) reviewContextFromWorkerLocked(rec OpsRunRecord) reviewOpsRunContext {
	if w, ok := d.workers[rec.WorkerID]; ok && w != nil && w.beadID == rec.BeadID {
		w.state = protocol.WorkerReviewing
		return reviewOpsRunContext{worktree: w.worktree, targetBranch: w.targetBranch, workerID: w.id, assignmentID: w.assignmentID}
	}
	return d.reviewContextFromAnyWorkerLocked(rec.BeadID)
}

func (d *Dispatcher) reviewContextFromAnyWorkerLocked(beadID string) reviewOpsRunContext {
	var reviewCtx reviewOpsRunContext
	for _, w := range d.workers {
		if w == nil || w.beadID != beadID {
			continue
		}
		reviewCtx.worktree = firstNonEmpty(reviewCtx.worktree, w.worktree)
		reviewCtx.targetBranch = firstNonEmpty(reviewCtx.targetBranch, w.targetBranch)
		if reviewCtx.workerID == "" {
			reviewCtx.workerID = w.id
			reviewCtx.assignmentID = w.assignmentID
		}
		if reviewCtx.worktree != "" && reviewCtx.targetBranch != "" {
			return reviewCtx
		}
	}
	return reviewCtx
}

func firstNonEmpty(current, candidate string) string {
	if current != "" {
		return current
	}
	return candidate
}

func (d *Dispatcher) beadContextForOpsRun(ctx context.Context, beadID string) (title, description string) {
	if d == nil || d.beads == nil || beadID == "" {
		return "", ""
	}
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		return "", ""
	}
	return detail.Title, detail.Description
}

func (d *Dispatcher) workdirForOpsRun(beadID string) string {
	if d == nil {
		return "."
	}
	d.mu.Lock()
	workdir := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if workdir != "" {
		return workdir
	}
	if d.repoRoot != "" {
		return d.repoRoot
	}
	return "."
}

func normalizeOpsRunRecord(rec OpsRunRecord) OpsRunRecord {
	if rec.Status == "" {
		rec.Status = opsRunStatusRunning
	}
	if rec.DispatcherPID == 0 {
		rec.DispatcherPID = os.Getpid()
	}
	fillOpsRunRuntimeModel(&rec)
	return rec
}

func fillOpsRunRuntimeModel(rec *OpsRunRecord) {
	if rec == nil || (rec.Runtime != "" && rec.Model != "") {
		return
	}
	runtime, model, _ := agentmodel.ResolveForRole(ops.Type(rec.Type).Role())
	if rec.Runtime == "" {
		rec.Runtime = runtime
	}
	if rec.Model == "" {
		rec.Model = model
	}
}

func validateOpsRunStatus(status string) error {
	switch status {
	case opsRunStatusRunning, opsRunStatusFailed, opsRunStatusStale, opsRunStatusResolved, opsRunStatusSuperseded:
		return nil
	default:
		return fmt.Errorf("invalid ops run status %q", status)
	}
}

func validateOpsRunCompletionStatus(status string) error {
	switch status {
	case opsRunStatusFailed, opsRunStatusStale, opsRunStatusResolved, opsRunStatusSuperseded:
		return nil
	default:
		return fmt.Errorf("invalid ops run completion status %q", status)
	}
}

func loadOpsRunByID(ctx context.Context, store opsRunStore, id int64) (OpsRunRecord, error) {
	rec, err := scanOpsRun(store.QueryRowContext(ctx, `
SELECT id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at
FROM ops_runs
WHERE id = ?`, id))
	if err != nil {
		return OpsRunRecord{}, fmt.Errorf("load ops run %d: %w", id, err)
	}
	return rec, nil
}

type opsRunScanner interface {
	Scan(dest ...any) error
}

func scanOpsRun(row opsRunScanner) (OpsRunRecord, error) {
	var (
		rec           OpsRunRecord
		escalationID  sql.NullInt64
		beadID        sql.NullString
		workerID      sql.NullString
		dispatcherPID sql.NullInt64
		processPID    sql.NullInt64
		runtime       sql.NullString
		model         sql.NullString
		verdict       sql.NullString
		feedback      sql.NullString
		errorText     sql.NullString
		startedAt     sql.NullString
		completedAt   sql.NullString
	)
	if err := row.Scan(
		&rec.ID, &escalationID, &rec.Type, &beadID, &workerID, &dispatcherPID,
		&processPID, &runtime, &model, &rec.Status, &verdict, &feedback,
		&errorText, &startedAt, &completedAt,
	); err != nil {
		return OpsRunRecord{}, fmt.Errorf("scan ops run: %w", err)
	}
	rec.EscalationID = escalationID.Int64
	rec.BeadID = beadID.String
	rec.WorkerID = workerID.String
	rec.DispatcherPID = int(dispatcherPID.Int64)
	rec.ProcessPID = int(processPID.Int64)
	rec.Runtime = runtime.String
	rec.Model = model.String
	rec.Verdict = verdict.String
	rec.Feedback = feedback.String
	rec.Error = errorText.String
	rec.StartedAt = startedAt.String
	rec.CompletedAt = completedAt.String
	return rec, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
