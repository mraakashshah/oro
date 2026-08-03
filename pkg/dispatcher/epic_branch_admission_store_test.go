package dispatcher //nolint:testpackage // white-box test exercises the package-private store contract

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestEpicBranchAdmissionLeaseStateMachineCAS(t *testing.T) {
	ctx := context.Background()
	db := openEpicBranchAdmissionTestDB(t, t.TempDir()+"/state.db")
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (id TEXT PRIMARY KEY, marker TEXT NOT NULL);
CREATE TABLE leases (id TEXT PRIMARY KEY, marker TEXT NOT NULL);
INSERT INTO recovery_quarantines (bead_id, reason, details, status)
VALUES ('oro-quarantine', 'existing', 'preserve me', 'open');
INSERT INTO runtime_leases VALUES ('runtime-lease', 'preserve me');
INSERT INTO leases VALUES ('storage-lease', 'preserve me');
`); err != nil {
		t.Fatalf("seed unrelated state: %v", err)
	}

	store := newEpicBranchAdmissionStore(db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if epicBranchAdmissionLeaseTTL != 2*time.Minute {
		t.Fatalf("lease TTL = %s, want 2m", epicBranchAdmissionLeaseTTL)
	}
	if epicBranchAdmissionLeaseRenewInterval != 30*time.Second {
		t.Fatalf("lease renewal interval = %s, want 30s", epicBranchAdmissionLeaseRenewInterval)
	}

	leaseA, acquired, err := store.acquire(ctx, "epic/oro-v0hx", "oro-v0hx", "main", "token-a", "worker-a", now)
	if err != nil {
		t.Fatalf("acquire absent branch for A: %v", err)
	}
	if !acquired {
		t.Fatal("acquire absent branch for A = busy, want acquired")
	}
	assertEpicBranchLease(t, leaseA, "leased", 1, "token-a", "worker-a", now.Add(2*time.Minute))

	busy, acquired, err := store.acquire(ctx, "epic/oro-v0hx", "oro-v0hx", "main", "token-b", "worker-b", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("contending acquire before expiry: %v", err)
	}
	if acquired {
		t.Fatal("contending acquire before expiry succeeded")
	}
	assertEpicBranchLease(t, busy, "leased", 1, "token-a", "worker-a", now.Add(2*time.Minute))

	if err := store.renew(ctx, leaseA.branch, "wrong-token", leaseA.generation, now.Add(30*time.Second)); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("renew with wrong token error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	if err := store.renew(ctx, leaseA.branch, leaseA.leaseToken, leaseA.generation+1, now.Add(30*time.Second)); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("renew with wrong generation error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	if err := store.renew(ctx, leaseA.branch, leaseA.leaseToken, leaseA.generation, now.Add(30*time.Second)); err != nil {
		t.Fatalf("renew A: %v", err)
	}

	busy, acquired, err = store.acquire(ctx, leaseA.branch, "oro-v0hx", "main", "token-b", "worker-b", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("contending acquire before renewed expiry: %v", err)
	}
	if acquired {
		t.Fatal("contending acquire before renewed expiry succeeded")
	}
	assertEpicBranchLease(t, busy, "leased", 1, "token-a", "worker-a", now.Add(2*time.Minute+30*time.Second))

	leaseB, acquired, err := store.acquire(ctx, leaseA.branch, "oro-v0hx", "main", "token-b", "worker-b", now.Add(2*time.Minute+30*time.Second))
	if err != nil {
		t.Fatalf("acquire expired lease for B: %v", err)
	}
	if !acquired {
		t.Fatal("acquire expired lease for B = busy, want acquired")
	}
	assertEpicBranchLease(t, leaseB, "leased", 2, "token-b", "worker-b", now.Add(4*time.Minute+30*time.Second))

	if _, err := store.block(ctx, leaseB.branch, leaseA.leaseToken, leaseA.generation, "checked_out", "/tmp/old", "old", "target", "oro-stale-recovery", "stale"); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("stale holder block error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	if err := store.release(ctx, leaseB.branch, leaseA.leaseToken, leaseA.generation, now.Add(3*time.Minute)); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("stale holder release error = %v, want ErrEpicBranchAdmissionCAS", err)
	}

	blocked, err := store.block(ctx, leaseB.branch, leaseB.leaseToken, leaseB.generation, "diverged", "/tmp/epic", "branch-sha", "target-sha", "oro-recovery", "preserve branch")
	if err != nil {
		t.Fatalf("block held branch: %v", err)
	}
	if blocked.state != "blocked" || blocked.targetBranch != "main" || blocked.blockerKind != "diverged" || blocked.checkoutPath != "/tmp/epic" || blocked.branchSHA != "branch-sha" || blocked.targetSHA != "target-sha" || blocked.recoveryBeadID != "oro-recovery" || blocked.details != "preserve branch" {
		t.Fatalf("blocked admission = %+v", blocked)
	}
	blockedAgain, err := store.block(ctx, leaseB.branch, leaseB.leaseToken, leaseB.generation, "diverged", "/tmp/epic", "branch-sha", "target-sha", "oro-recovery", "preserve branch")
	if err != nil {
		t.Fatalf("repeat identical block: %v", err)
	}
	if blockedAgain != blocked {
		t.Fatalf("repeated block changed row\ngot:  %+v\nwant: %+v", blockedAgain, blocked)
	}
	if _, err := store.block(ctx, leaseB.branch, leaseB.leaseToken, leaseB.generation, "diverged", "/tmp/epic", "branch-sha", "target-sha", "oro-other-recovery", "preserve branch"); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("mismatched recovery link block error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	var branchRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM epic_branch_admissions WHERE branch=?`, leaseB.branch).Scan(&branchRows); err != nil {
		t.Fatalf("count blocked branch rows: %v", err)
	}
	if branchRows != 1 {
		t.Fatalf("blocked branch rows = %d, want 1", branchRows)
	}
	if _, acquired, err := store.acquire(ctx, leaseB.branch, "oro-v0hx", "main", "token-c", "worker-c", now.Add(24*time.Hour)); err != nil || acquired {
		t.Fatalf("acquire blocked branch = acquired %v, error %v; want busy without error", acquired, err)
	}

	safe, acquired, err := store.acquire(ctx, "epic/oro-safe", "oro-safe", "main", "token-safe", "worker-safe", now)
	if err != nil || !acquired {
		t.Fatalf("acquire safe branch = acquired %v, error %v", acquired, err)
	}
	if err := store.release(ctx, safe.branch, safe.leaseToken, safe.generation, now.Add(time.Minute)); err != nil {
		t.Fatalf("release safe lease: %v", err)
	}
	if err := store.release(ctx, safe.branch, safe.leaseToken, safe.generation, now.Add(time.Minute)); err != nil {
		t.Fatalf("repeat safe release: %v", err)
	}
	var releasedState string
	var releasedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, resolved_at FROM epic_branch_admissions WHERE branch=?`, safe.branch).Scan(&releasedState, &releasedAt); err != nil {
		t.Fatalf("read released admission: %v", err)
	}
	if releasedState != "resolved" || !releasedAt.Valid {
		t.Fatalf("released admission state = %q, resolved_at = %v", releasedState, releasedAt)
	}
	reused, acquired, err := store.acquire(ctx, safe.branch, "oro-safe", "main", "token-next", "worker-next", now.Add(90*time.Second))
	if err != nil || !acquired {
		t.Fatalf("reuse resolved branch = acquired %v, error %v", acquired, err)
	}
	assertEpicBranchLease(t, reused, "leased", 2, "token-next", "worker-next", now.Add(3*time.Minute+30*time.Second))

	for name, query := range map[string]string{
		"quarantine":    `SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id='oro-quarantine' AND reason='existing' AND details='preserve me' AND status='open'`,
		"runtime lease": `SELECT COUNT(*) FROM runtime_leases WHERE id='runtime-lease' AND marker='preserve me'`,
		"storage lease": `SELECT COUNT(*) FROM leases WHERE id='storage-lease' AND marker='preserve me'`,
	} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("count preserved %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("preserved %s rows = %d, want 1", name, count)
		}
	}
}

func TestEpicBranchAdmissionAcquireConcurrent(t *testing.T) {
	ctx := context.Background()
	db := openEpicBranchAdmissionTestDB(t, t.TempDir()+"/state.db")
	defer func() { _ = db.Close() }()
	store := newEpicBranchAdmissionStore(db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	type result struct {
		admission epicBranchAdmission
		acquired  bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, holder := range []struct{ token, owner string }{{"token-a", "worker-a"}, {"token-b", "worker-b"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			admission, acquired, err := store.acquire(ctx, "epic/oro-race", "oro-race", "main", holder.token, holder.owner, now)
			results <- result{admission: admission, acquired: acquired, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	acquiredCount := 0
	for got := range results {
		if got.err != nil {
			t.Errorf("concurrent acquire: %v", got.err)
			continue
		}
		if got.admission.generation != 1 || got.admission.state != "leased" {
			t.Errorf("concurrent admission = %+v", got.admission)
		}
		if got.acquired {
			acquiredCount++
		}
	}
	if acquiredCount != 1 {
		t.Fatalf("concurrent acquired count = %d, want 1", acquiredCount)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM epic_branch_admissions WHERE branch='epic/oro-race'`).Scan(&rows); err != nil {
		t.Fatalf("count concurrent admission rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("concurrent admission rows = %d, want 1", rows)
	}
}

func TestEpicBranchAdmissionExpiredHolderCannotBlock(t *testing.T) {
	ctx := context.Background()
	db := openEpicBranchAdmissionTestDB(t, t.TempDir()+"/state.db")
	defer func() { _ = db.Close() }()
	store := newEpicBranchAdmissionStore(db)
	now := time.Now().UTC()
	lease, acquired, err := store.acquire(ctx, "epic/oro-expired-block", "oro-expired-block", "main", "token-a", "worker-a", now)
	if err != nil || !acquired {
		t.Fatalf("acquire lease = acquired %v, error %v", acquired, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE epic_branch_admissions SET lease_expires_at='2000-01-01T00:00:00Z' WHERE branch=?`, lease.branch); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	if _, err := store.block(ctx, lease.branch, lease.leaseToken, lease.generation, "diverged", "/tmp/expired", "old", "target", "oro-expired-recovery", "must not persist"); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("expired holder block error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	var state string
	var blockerKind, recoveryBeadID sql.NullString
	var details string
	if err := db.QueryRowContext(ctx, `
SELECT state, blocker_kind, recovery_bead_id, details
FROM epic_branch_admissions WHERE branch=?`, lease.branch).Scan(&state, &blockerKind, &recoveryBeadID, &details); err != nil {
		t.Fatalf("read expired lease after block attempt: %v", err)
	}
	if state != "leased" || blockerKind.Valid || recoveryBeadID.Valid || details != "" {
		t.Fatalf("expired block mutated row: state=%q blocker=%v recovery=%v details=%q", state, blockerKind, recoveryBeadID, details)
	}
}

func TestEpicBranchAdmissionBlockedResolveCAS(t *testing.T) {
	ctx := context.Background()
	db := openEpicBranchAdmissionTestDB(t, t.TempDir()+"/state.db")
	defer func() { _ = db.Close() }()
	store := newEpicBranchAdmissionStore(db)
	now := time.Now().UTC()
	lease, acquired, err := store.acquire(ctx, "epic/oro-resolve-block", "oro-resolve-block", "main", "token-a", "worker-a", now)
	if err != nil || !acquired {
		t.Fatalf("acquire lease = acquired %v, error %v", acquired, err)
	}
	blocked, err := store.block(ctx, lease.branch, lease.leaseToken, lease.generation, "diverged", "/tmp/blocked", "abc", "def", "oro-recovery", "repair branch")
	if err != nil {
		t.Fatalf("block lease: %v", err)
	}

	resolvedAt := now.Add(time.Minute)
	if err := store.resolve(ctx, blocked.branch, blocked.generation+1, resolvedAt); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("resolve with stale generation error = %v, want ErrEpicBranchAdmissionCAS", err)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM epic_branch_admissions WHERE branch=?`, blocked.branch).Scan(&state); err != nil {
		t.Fatalf("read state after stale resolve: %v", err)
	}
	if state != "blocked" {
		t.Fatalf("stale resolve state = %q, want blocked", state)
	}
	if err := store.resolve(ctx, blocked.branch, blocked.generation, resolvedAt); err != nil {
		t.Fatalf("resolve blocked admission: %v", err)
	}
	if err := store.resolve(ctx, blocked.branch, blocked.generation, resolvedAt); err != nil {
		t.Fatalf("repeat identical resolve: %v", err)
	}

	var leaseToken, leaseOwner, leaseExpiresAt sql.NullString
	var generation int64
	var recoveryBeadID, details, resolvedAtText string
	if err := db.QueryRowContext(ctx, `
SELECT state, generation, lease_token, lease_owner, lease_expires_at,
       recovery_bead_id, details, resolved_at
FROM epic_branch_admissions WHERE branch=?`, blocked.branch).Scan(
		&state, &generation, &leaseToken, &leaseOwner, &leaseExpiresAt,
		&recoveryBeadID, &details, &resolvedAtText,
	); err != nil {
		t.Fatalf("read resolved blocker: %v", err)
	}
	if state != "resolved" || generation != blocked.generation || leaseToken.Valid || leaseOwner.Valid || leaseExpiresAt.Valid || recoveryBeadID != "oro-recovery" || details != "repair branch" || resolvedAtText != formatEpicBranchAdmissionTime(resolvedAt) {
		t.Fatalf("resolved blocker = state %q generation %d token %v owner %v expiry %v recovery %q details %q resolved_at %q", state, generation, leaseToken, leaseOwner, leaseExpiresAt, recoveryBeadID, details, resolvedAtText)
	}
	if err := store.resolve(ctx, blocked.branch, blocked.generation+1, resolvedAt); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("resolve resolved row with stale generation error = %v, want ErrEpicBranchAdmissionCAS", err)
	}

	safe, acquired, err := store.acquire(ctx, "epic/oro-safe-resolve", "oro-safe-resolve", "main", "token-safe", "worker-safe", now)
	if err != nil || !acquired {
		t.Fatalf("acquire safe lease = acquired %v, error %v", acquired, err)
	}
	if err := store.release(ctx, safe.branch, safe.leaseToken, safe.generation, now.Add(30*time.Second)); err != nil {
		t.Fatalf("release safe lease: %v", err)
	}
	if err := store.resolve(ctx, safe.branch, safe.generation, resolvedAt); !errors.Is(err, ErrEpicBranchAdmissionCAS) {
		t.Fatalf("resolve safely released row error = %v, want ErrEpicBranchAdmissionCAS", err)
	}

	checkedOut, acquired, err := store.acquire(ctx, "epic/oro-checked-out-resolve", "oro-checked-out-resolve", "main", "token-checked-out", "worker-checked-out", now)
	if err != nil || !acquired {
		t.Fatalf("acquire checked-out lease = acquired %v, error %v", acquired, err)
	}
	checkedOut, err = store.block(ctx, checkedOut.branch, checkedOut.leaseToken, checkedOut.generation, "checked_out", "/tmp/checked-out", "abc", "def", "", "branch is checked out")
	if err != nil {
		t.Fatalf("block checked-out branch without recovery child: %v", err)
	}
	var nullableRecovery sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT recovery_bead_id FROM epic_branch_admissions WHERE branch=?`, checkedOut.branch).Scan(&nullableRecovery); err != nil {
		t.Fatalf("read checked-out recovery link: %v", err)
	}
	if nullableRecovery.Valid {
		t.Fatalf("checked-out recovery link = %q, want NULL", nullableRecovery.String)
	}
	if err := store.resolve(ctx, checkedOut.branch, checkedOut.generation, resolvedAt); err != nil {
		t.Fatalf("resolve checked-out blocker: %v", err)
	}
	if err := store.resolve(ctx, checkedOut.branch, checkedOut.generation, resolvedAt); err != nil {
		t.Fatalf("repeat checked-out blocker resolve: %v", err)
	}
}

func TestEpicBranchAdmissionPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	db := openEpicBranchAdmissionTestDB(t, path)
	store := newEpicBranchAdmissionStore(db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	first, acquired, err := store.acquire(ctx, "epic/oro-reopen", "oro-reopen", "main", "token-a", "worker-a", now)
	if err != nil || !acquired {
		t.Fatalf("initial acquire = acquired %v, error %v", acquired, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close initial db: %v", err)
	}

	db = openEpicBranchAdmissionTestDB(t, path)
	defer func() { _ = db.Close() }()
	store = newEpicBranchAdmissionStore(db)
	persisted, acquired, err := store.acquire(ctx, first.branch, first.epicID, first.targetBranch, "token-b", "worker-b", now.Add(time.Minute))
	if err != nil || acquired {
		t.Fatalf("acquire live lease after reopen = acquired %v, error %v", acquired, err)
	}
	assertEpicBranchLease(t, persisted, "leased", 1, "token-a", "worker-a", now.Add(2*time.Minute))
	reclaimed, acquired, err := store.acquire(ctx, first.branch, first.epicID, first.targetBranch, "token-b", "worker-b", now.Add(2*time.Minute))
	if err != nil || !acquired {
		t.Fatalf("reclaim expired lease after reopen = acquired %v, error %v", acquired, err)
	}
	assertEpicBranchLease(t, reclaimed, "leased", 2, "token-b", "worker-b", now.Add(4*time.Minute))
}

func TestEpicBranchAdmissionDBErrorsFailClosed(t *testing.T) {
	ctx := context.Background()
	db := openEpicBranchAdmissionTestDB(t, t.TempDir()+"/state.db")
	store := newEpicBranchAdmissionStore(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	if _, acquired, err := store.acquire(ctx, "epic/oro-closed", "oro-closed", "main", "token", "worker", now); err == nil || acquired {
		t.Fatalf("acquire closed DB = acquired %v, error %v; want fail closed", acquired, err)
	}
	if err := store.renew(ctx, "epic/oro-closed", "token", 1, now); err == nil {
		t.Fatal("renew closed DB succeeded")
	}
	if _, err := store.block(ctx, "epic/oro-closed", "token", 1, "diverged", "/tmp/epic", "abc", "def", "oro-recovery", "details"); err == nil {
		t.Fatal("block closed DB succeeded")
	}
	if err := store.release(ctx, "epic/oro-closed", "token", 1, now); err == nil {
		t.Fatal("release closed DB succeeded")
	}
	if err := store.resolve(ctx, "epic/oro-closed", 1, now); err == nil {
		t.Fatal("resolve closed DB succeeded")
	}
}

func openEpicBranchAdmissionTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open state DB: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		_ = db.Close()
		t.Fatalf("migrate runtime schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate bead schema: %v", err)
	}
	return db
}

func assertEpicBranchLease(t *testing.T, got epicBranchAdmission, state string, generation int64, token, owner string, expiresAt time.Time) {
	t.Helper()
	if got.state != state || got.generation != generation || got.leaseToken != token || got.leaseOwner != owner || !got.leaseExpiresAt.Equal(expiresAt) {
		t.Fatalf("admission = %+v, want state=%s generation=%d token=%q owner=%q expiry=%s", got, state, generation, token, owner, expiresAt)
	}
}
