package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	epicBranchAdmissionLeaseTTL           = 2 * time.Minute
	epicBranchAdmissionLeaseRenewInterval = 30 * time.Second
)

// ErrEpicBranchAdmissionCAS reports that the caller no longer holds the
// token-and-generation identity required for an admission mutation.
var ErrEpicBranchAdmissionCAS = errors.New("epic branch admission compare-and-swap failed")

type epicBranchAdmission struct {
	branch         string
	epicID         string
	targetBranch   string
	state          string
	generation     int64
	leaseToken     string
	leaseOwner     string
	leaseExpiresAt time.Time
	blockerKind    string
	checkoutPath   string
	branchSHA      string
	targetSHA      string
	recoveryBeadID string
	details        string
	createdAt      time.Time
	updatedAt      time.Time
	resolvedAt     time.Time
}

type epicBranchAdmissionStore struct {
	db *sql.DB
}

type epicBranchLeaseRequest struct {
	branch       string
	epicID       string
	targetBranch string
	leaseToken   string
	leaseOwner   string
	now          time.Time
}

func newEpicBranchAdmissionStore(db *sql.DB) *epicBranchAdmissionStore {
	return &epicBranchAdmissionStore{db: db}
}

func (s *epicBranchAdmissionStore) acquire(
	ctx context.Context,
	branch, epicID, targetBranch, leaseToken, leaseOwner string,
	now time.Time,
) (epicBranchAdmission, bool, error) {
	if s == nil || s.db == nil {
		return epicBranchAdmission{}, false, errors.New("acquire epic branch admission: store is nil")
	}
	if blank(branch, epicID, targetBranch, leaseToken, leaseOwner) || now.IsZero() {
		return epicBranchAdmission{}, false, errors.New("acquire epic branch admission: missing lease identity")
	}
	conn, err := beginEpicBranchAdmissionImmediate(ctx, s.db, "acquire")
	if err != nil {
		return epicBranchAdmission{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
		_ = conn.Close()
	}()
	request := epicBranchLeaseRequest{
		branch: branch, epicID: epicID, targetBranch: targetBranch,
		leaseToken: leaseToken, leaseOwner: leaseOwner, now: now,
	}
	admission, acquired, err := acquireEpicBranchAdmissionTx(ctx, conn, request)
	if err != nil {
		return epicBranchAdmission{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return epicBranchAdmission{}, false, fmt.Errorf("acquire epic branch admission %q: commit: %w", branch, err)
	}
	committed = true
	return admission, acquired, nil
}

func acquireEpicBranchAdmissionTx(ctx context.Context, conn *sql.Conn, request epicBranchLeaseRequest) (epicBranchAdmission, bool, error) {
	acquired, err := insertEpicBranchAdmission(ctx, conn, request)
	if err != nil {
		return epicBranchAdmission{}, false, err
	}
	if !acquired {
		acquired, err = reclaimEpicBranchAdmission(ctx, conn, request)
		if err != nil {
			return epicBranchAdmission{}, false, err
		}
	}
	admission, err := loadEpicBranchAdmission(ctx, conn, request.branch)
	if err != nil {
		return epicBranchAdmission{}, false, fmt.Errorf("acquire epic branch admission %q: load: %w", request.branch, err)
	}
	return admission, acquired, nil
}

func insertEpicBranchAdmission(ctx context.Context, conn *sql.Conn, request epicBranchLeaseRequest) (bool, error) {
	nowText := formatEpicBranchAdmissionTime(request.now)
	result, err := conn.ExecContext(ctx, `
INSERT OR IGNORE INTO epic_branch_admissions (
    branch, epic_id, target_branch, state, generation, lease_token, lease_owner,
    lease_expires_at, created_at, updated_at
) VALUES (?, ?, ?, 'leased', 1, ?, ?, ?, ?, ?)`,
		request.branch, request.epicID, request.targetBranch, request.leaseToken, request.leaseOwner,
		formatEpicBranchAdmissionTime(request.now.Add(epicBranchAdmissionLeaseTTL)), nowText, nowText)
	if err != nil {
		return false, fmt.Errorf("acquire epic branch admission %q: insert: %w", request.branch, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire epic branch admission %q: count insert: %w", request.branch, err)
	}
	return inserted == 1, nil
}

func reclaimEpicBranchAdmission(ctx context.Context, conn *sql.Conn, request epicBranchLeaseRequest) (bool, error) {
	nowText := formatEpicBranchAdmissionTime(request.now)
	result, err := conn.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET epic_id = ?,
    target_branch = ?,
    state = 'leased',
    generation = generation + 1,
    lease_token = ?,
    lease_owner = ?,
    lease_expires_at = ?,
    blocker_kind = NULL,
    checkout_path = NULL,
    branch_sha = '',
    target_sha = '',
    recovery_bead_id = NULL,
    details = '',
    updated_at = ?,
    resolved_at = NULL
WHERE branch = ?
  AND (
      state = 'resolved'
      OR (
          state = 'leased'
          AND lease_expires_at IS NOT NULL
          AND julianday(lease_expires_at) <= julianday(?)
      )
  )`, request.epicID, request.targetBranch, request.leaseToken, request.leaseOwner,
		formatEpicBranchAdmissionTime(request.now.Add(epicBranchAdmissionLeaseTTL)), nowText, request.branch, nowText)
	if err != nil {
		return false, fmt.Errorf("acquire epic branch admission %q: reclaim: %w", request.branch, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire epic branch admission %q: count reclaim: %w", request.branch, err)
	}
	return updated == 1, nil
}

func (s *epicBranchAdmissionStore) renew(ctx context.Context, branch, leaseToken string, generation int64, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("renew epic branch admission: store is nil")
	}
	if blank(branch, leaseToken) || generation <= 0 || now.IsZero() {
		return errors.New("renew epic branch admission: missing lease identity")
	}
	nowText := formatEpicBranchAdmissionTime(now)
	result, err := s.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET lease_expires_at = ?, updated_at = ?
WHERE branch = ?
  AND state = 'leased'
  AND lease_token = ?
  AND generation = ?
  AND lease_expires_at IS NOT NULL
  AND julianday(lease_expires_at) > julianday(?)`,
		formatEpicBranchAdmissionTime(now.Add(epicBranchAdmissionLeaseTTL)), nowText,
		branch, leaseToken, generation, nowText)
	if err != nil {
		return fmt.Errorf("renew epic branch admission %q: %w", branch, err)
	}
	return requireEpicBranchAdmissionCAS(result, "renew", branch)
}

func (s *epicBranchAdmissionStore) block(
	ctx context.Context, branch, leaseToken string,
	generation int64, blockerKind, checkoutPath, branchSHA, targetSHA, recoveryBeadID, details string,
) (epicBranchAdmission, error) {
	if s == nil || s.db == nil {
		return epicBranchAdmission{}, errors.New("block epic branch admission: store is nil")
	}
	if blank(branch, leaseToken, blockerKind) || generation <= 0 {
		return epicBranchAdmission{}, errors.New("block epic branch admission: missing lease or blocker identity")
	}

	conn, err := beginEpicBranchAdmissionImmediate(ctx, s.db, "block")
	if err != nil {
		return epicBranchAdmission{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
		_ = conn.Close()
	}()

	result, err := conn.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET state = 'blocked',
    blocker_kind = ?,
    checkout_path = NULLIF(?, ''),
    branch_sha = ?,
    target_sha = ?,
    recovery_bead_id = NULLIF(?, ''),
    details = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE branch = ?
  AND state = 'leased'
  AND lease_token = ?
  AND generation = ?
  AND lease_expires_at IS NOT NULL
  AND julianday(lease_expires_at) > julianday('now')`, blockerKind, checkoutPath, branchSHA, targetSHA, recoveryBeadID, details, branch, leaseToken, generation)
	if err != nil {
		return epicBranchAdmission{}, fmt.Errorf("block epic branch admission %q: update: %w", branch, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return epicBranchAdmission{}, fmt.Errorf("block epic branch admission %q: count update: %w", branch, err)
	}
	admission, err := loadEpicBranchAdmission(ctx, conn, branch)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return epicBranchAdmission{}, epicBranchAdmissionCASError("block", branch)
		}
		return epicBranchAdmission{}, fmt.Errorf("block epic branch admission %q: load: %w", branch, err)
	}
	if changed != 1 && !sameEpicBranchBlock(admission, leaseToken, generation, blockerKind, checkoutPath, branchSHA, targetSHA, recoveryBeadID, details) {
		return epicBranchAdmission{}, epicBranchAdmissionCASError("block", branch)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return epicBranchAdmission{}, fmt.Errorf("block epic branch admission %q: commit: %w", branch, err)
	}
	committed = true
	return admission, nil
}

func (s *epicBranchAdmissionStore) release(ctx context.Context, branch, leaseToken string, generation int64, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("release epic branch admission: store is nil")
	}
	if blank(branch, leaseToken) || generation <= 0 || now.IsZero() {
		return errors.New("release epic branch admission: missing lease identity")
	}
	nowText := formatEpicBranchAdmissionTime(now)
	result, err := s.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET state = 'resolved',
    lease_owner = NULL,
    lease_expires_at = NULL,
    blocker_kind = NULL,
    checkout_path = NULL,
    branch_sha = '',
    target_sha = '',
    recovery_bead_id = NULL,
    details = '',
    updated_at = ?,
    resolved_at = ?
WHERE branch = ?
  AND state = 'leased'
  AND lease_token = ?
  AND generation = ?
  AND lease_expires_at IS NOT NULL
  AND julianday(lease_expires_at) > julianday(?)`,
		nowText, nowText, branch, leaseToken, generation, nowText)
	if err != nil {
		return fmt.Errorf("release epic branch admission %q: %w", branch, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("release epic branch admission %q: count update: %w", branch, err)
	}
	if changed == 1 {
		return nil
	}

	var state string
	var currentGeneration int64
	var currentToken sql.NullString
	err = s.db.QueryRowContext(ctx, `
SELECT state, generation, lease_token
FROM epic_branch_admissions
WHERE branch = ?`, branch).Scan(&state, &currentGeneration, &currentToken)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("release epic branch admission %q: inspect conflict: %w", branch, err)
	}
	if err == nil && state == "resolved" && currentGeneration == generation && currentToken.String == leaseToken {
		return nil
	}
	return epicBranchAdmissionCASError("release", branch)
}

func (s *epicBranchAdmissionStore) resolve(ctx context.Context, branch string, generation int64, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("resolve epic branch admission: store is nil")
	}
	if blank(branch) || generation <= 0 || now.IsZero() {
		return errors.New("resolve epic branch admission: missing blocker identity")
	}
	nowText := formatEpicBranchAdmissionTime(now)
	result, err := s.db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET state = 'resolved',
    lease_token = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = ?,
    resolved_at = ?
WHERE branch = ?
  AND state = 'blocked'
  AND generation = ?`, nowText, nowText, branch, generation)
	if err != nil {
		return fmt.Errorf("resolve epic branch admission %q: %w", branch, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve epic branch admission %q: count update: %w", branch, err)
	}
	if changed == 1 {
		return nil
	}

	idempotent, err := s.isIdempotentBlockedResolve(ctx, branch, generation)
	if err != nil {
		return fmt.Errorf("resolve epic branch admission %q: inspect conflict: %w", branch, err)
	}
	if idempotent {
		return nil
	}
	return epicBranchAdmissionCASError("resolve", branch)
}

func (s *epicBranchAdmissionStore) isIdempotentBlockedResolve(ctx context.Context, branch string, generation int64) (bool, error) {
	var state string
	var currentGeneration int64
	var blockerKind sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT state, generation, blocker_kind
FROM epic_branch_admissions
WHERE branch = ?`, branch).Scan(&state, &currentGeneration, &blockerKind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query resolved epic branch admission: %w", err)
	}
	return state == "resolved" && currentGeneration == generation && blockerKind.Valid && blockerKind.String != "", nil
}

type epicBranchAdmissionRow interface {
	Scan(...any) error
}

type epicBranchAdmissionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadEpicBranchAdmission(ctx context.Context, querier epicBranchAdmissionQuerier, branch string) (epicBranchAdmission, error) {
	return scanEpicBranchAdmission(querier.QueryRowContext(ctx, `
SELECT branch, epic_id, target_branch, state, generation,
       lease_token, lease_owner, lease_expires_at, blocker_kind, checkout_path,
       branch_sha, target_sha, recovery_bead_id, details, created_at, updated_at, resolved_at
FROM epic_branch_admissions
WHERE branch = ?`, branch))
}

func scanEpicBranchAdmission(row epicBranchAdmissionRow) (epicBranchAdmission, error) {
	var admission epicBranchAdmission
	var leaseToken, leaseOwner, leaseExpiresAt, blockerKind, checkoutPath, recoveryBeadID, resolvedAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&admission.branch,
		&admission.epicID,
		&admission.targetBranch,
		&admission.state,
		&admission.generation,
		&leaseToken,
		&leaseOwner,
		&leaseExpiresAt,
		&blockerKind,
		&checkoutPath,
		&admission.branchSHA,
		&admission.targetSHA,
		&recoveryBeadID,
		&admission.details,
		&createdAt,
		&updatedAt,
		&resolvedAt,
	); err != nil {
		return epicBranchAdmission{}, fmt.Errorf("scan epic branch admission row: %w", err)
	}
	admission.leaseToken = leaseToken.String
	admission.leaseOwner = leaseOwner.String
	admission.blockerKind = blockerKind.String
	admission.checkoutPath = checkoutPath.String
	admission.recoveryBeadID = recoveryBeadID.String
	var err error
	if admission.createdAt, err = parseEpicBranchAdmissionTime(createdAt); err != nil {
		return epicBranchAdmission{}, fmt.Errorf("parse created_at: %w", err)
	}
	if admission.updatedAt, err = parseEpicBranchAdmissionTime(updatedAt); err != nil {
		return epicBranchAdmission{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if leaseExpiresAt.Valid {
		if admission.leaseExpiresAt, err = parseEpicBranchAdmissionTime(leaseExpiresAt.String); err != nil {
			return epicBranchAdmission{}, fmt.Errorf("parse lease_expires_at: %w", err)
		}
	}
	if resolvedAt.Valid {
		if admission.resolvedAt, err = parseEpicBranchAdmissionTime(resolvedAt.String); err != nil {
			return epicBranchAdmission{}, fmt.Errorf("parse resolved_at: %w", err)
		}
	}
	return admission, nil
}

func beginEpicBranchAdmissionImmediate(ctx context.Context, db *sql.DB, operation string) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s epic branch admission: open connection: %w", operation, err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s epic branch admission: set busy timeout: %w", operation, err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s epic branch admission: begin immediate: %w", operation, err)
	}
	return conn, nil
}

func requireEpicBranchAdmissionCAS(result sql.Result, operation, branch string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s epic branch admission %q: count update: %w", operation, branch, err)
	}
	if changed != 1 {
		return epicBranchAdmissionCASError(operation, branch)
	}
	return nil
}

func epicBranchAdmissionCASError(operation, branch string) error {
	return fmt.Errorf("%s epic branch admission %q: %w", operation, branch, ErrEpicBranchAdmissionCAS)
}

func sameEpicBranchBlock(
	admission epicBranchAdmission,
	leaseToken string,
	generation int64,
	blockerKind, checkoutPath, branchSHA, targetSHA, recoveryBeadID, details string,
) bool {
	return admission.state == "blocked" &&
		admission.leaseToken == leaseToken &&
		admission.generation == generation &&
		admission.blockerKind == blockerKind &&
		admission.checkoutPath == checkoutPath &&
		admission.branchSHA == branchSHA &&
		admission.targetSHA == targetSHA &&
		admission.recoveryBeadID == recoveryBeadID &&
		admission.details == details
}

func formatEpicBranchAdmissionTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseEpicBranchAdmissionTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed.UTC(), nil
	}
	parsed, sqliteErr := time.Parse("2006-01-02 15:04:05", value)
	if sqliteErr == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("parse epic branch admission time %q: %w", value, err)
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
