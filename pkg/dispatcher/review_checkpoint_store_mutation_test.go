package dispatcher //nolint:testpackage // mutation tests exercise white-box checkpoint transactions

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"

	modernsqlite "modernc.org/sqlite"
)

var (
	reviewCheckpointMutationDriverID atomic.Uint64
	errCheckpointBegin               = errors.New("injected checkpoint begin failure")
	errCheckpointExec                = errors.New("injected checkpoint exec failure")
	errCheckpointQuery               = errors.New("injected checkpoint query failure")
	errCheckpointRows                = errors.New("injected checkpoint rows failure")
	errCheckpointRowsAffected        = errors.New("injected checkpoint rows-affected failure")
	errCheckpointCommit              = errors.New("injected checkpoint commit failure")
)

type reviewCheckpointMutationFaults struct {
	mu sync.Mutex

	beginErr    error
	beforeBegin func(driver.ExecerContext) error

	execNeedle string
	execErr    error

	queryNeedle string
	queryAt     int
	querySeen   int
	queryErr    error

	resultNeedle    string
	rowsAffectedErr error

	scanNeedle  string
	nextNeedle  string
	closeNeedle string
	closeErr    error
	closeCount  int

	commitErr error
}

func (f *reviewCheckpointMutationFaults) configure(change func(*reviewCheckpointMutationFaults)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *reviewCheckpointMutationFaults) clear() {
	f.configure(func(f *reviewCheckpointMutationFaults) {
		f.beginErr = nil
		f.beforeBegin = nil
		f.execNeedle = ""
		f.execErr = nil
		f.queryNeedle = ""
		f.queryAt = 0
		f.querySeen = 0
		f.queryErr = nil
		f.resultNeedle = ""
		f.rowsAffectedErr = nil
		f.scanNeedle = ""
		f.nextNeedle = ""
		f.closeNeedle = ""
		f.closeErr = nil
		f.closeCount = 0
		f.commitErr = nil
	})
}

func (f *reviewCheckpointMutationFaults) closeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCount
}

type reviewCheckpointMutationDriver struct {
	base   driver.Driver
	faults *reviewCheckpointMutationFaults
}

func (d *reviewCheckpointMutationDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &reviewCheckpointMutationConn{Conn: conn, faults: d.faults}, nil
}

type reviewCheckpointMutationConn struct {
	driver.Conn
	faults *reviewCheckpointMutationFaults
}

func (c *reviewCheckpointMutationConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.faults.mu.Lock()
	beginErr := c.faults.beginErr
	beforeBegin := c.faults.beforeBegin
	commitErr := c.faults.commitErr
	c.faults.mu.Unlock()
	if beforeBegin != nil {
		execer, ok := c.Conn.(driver.ExecerContext)
		if !ok {
			return nil, errors.New("SQLite connection does not support ExecContext")
		}
		if err := beforeBegin(execer); err != nil {
			return nil, err
		}
	}
	if beginErr != nil {
		return nil, beginErr
	}
	conn, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("SQLite connection does not support BeginTx")
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &reviewCheckpointMutationTx{Tx: tx, commitErr: commitErr}, nil
}

func (c *reviewCheckpointMutationConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	c.faults.mu.Lock()
	execErr := error(nil)
	if c.faults.execNeedle != "" && strings.Contains(query, c.faults.execNeedle) {
		execErr = c.faults.execErr
	}
	resultNeedle := c.faults.resultNeedle
	rowsAffectedErr := c.faults.rowsAffectedErr
	c.faults.mu.Unlock()
	if execErr != nil {
		return nil, execErr
	}
	conn, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	result, err := conn.ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if resultNeedle != "" && strings.Contains(query, resultNeedle) {
		return reviewCheckpointMutationResult{Result: result, rowsAffectedErr: rowsAffectedErr}, nil
	}
	return result, nil
}

func (c *reviewCheckpointMutationConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	c.faults.mu.Lock()
	queryErr := error(nil)
	if c.faults.queryNeedle != "" && strings.Contains(query, c.faults.queryNeedle) {
		c.faults.querySeen++
		if c.faults.queryAt == 0 || c.faults.querySeen == c.faults.queryAt {
			queryErr = c.faults.queryErr
		}
	}
	scan := c.faults.scanNeedle != "" && strings.Contains(query, c.faults.scanNeedle)
	nextErr := c.faults.nextNeedle != "" && strings.Contains(query, c.faults.nextNeedle)
	closeMatch := c.faults.closeNeedle != "" && strings.Contains(query, c.faults.closeNeedle)
	closeErr := c.faults.closeErr
	c.faults.mu.Unlock()
	if queryErr != nil {
		return nil, queryErr
	}
	conn, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := conn.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &reviewCheckpointMutationRows{
		Rows: rows, faults: c.faults, scan: scan, nextErr: nextErr,
		trackClose: closeMatch, closeErr: closeErr,
	}, nil
}

func (c *reviewCheckpointMutationConn) CheckNamedValue(value *driver.NamedValue) error {
	if conn, ok := c.Conn.(driver.NamedValueChecker); ok {
		return conn.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *reviewCheckpointMutationConn) Ping(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.Pinger); ok {
		return conn.Ping(ctx)
	}
	return nil
}

func (c *reviewCheckpointMutationConn) ResetSession(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.SessionResetter); ok {
		return conn.ResetSession(ctx)
	}
	return nil
}

func (c *reviewCheckpointMutationConn) IsValid() bool {
	if conn, ok := c.Conn.(driver.Validator); ok {
		return conn.IsValid()
	}
	return true
}

type reviewCheckpointMutationTx struct {
	driver.Tx
	commitErr error
}

func (tx *reviewCheckpointMutationTx) Commit() error {
	if tx.commitErr != nil {
		_ = tx.Rollback()
		return tx.commitErr
	}
	return tx.Tx.Commit()
}

type reviewCheckpointMutationResult struct {
	driver.Result
	rowsAffectedErr error
}

func (r reviewCheckpointMutationResult) RowsAffected() (int64, error) {
	if r.rowsAffectedErr != nil {
		return 0, r.rowsAffectedErr
	}
	return r.Result.RowsAffected()
}

type reviewCheckpointMutationRows struct {
	driver.Rows
	faults     *reviewCheckpointMutationFaults
	scan       bool
	nextErr    bool
	trackClose bool
	closeErr   error
}

func (r *reviewCheckpointMutationRows) Next(dest []driver.Value) error {
	if r.nextErr {
		r.nextErr = false
		return errCheckpointRows
	}
	err := r.Rows.Next(dest)
	if err == nil && r.scan && len(dest) > 0 {
		r.scan = false
		dest[0] = "not-an-integer"
	}
	return err
}

func (r *reviewCheckpointMutationRows) Close() error {
	err := r.Rows.Close()
	if r.trackClose {
		r.faults.mu.Lock()
		r.faults.closeCount++
		r.faults.mu.Unlock()
	}
	if r.closeErr != nil {
		return r.closeErr
	}
	return err
}

func openReviewCheckpointMutationStore(
	t *testing.T,
) (*ReviewCheckpointStore, *reviewCheckpointMutationFaults) {
	t.Helper()
	faults := &reviewCheckpointMutationFaults{}
	driverName := fmt.Sprintf("oro-review-checkpoint-mutation-%d", reviewCheckpointMutationDriverID.Add(1))
	sql.Register(driverName, &reviewCheckpointMutationDriver{base: &modernsqlite.Driver{}, faults: faults})
	db, err := sql.Open(driverName, filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatalf("open checkpoint mutation database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := protocol.MigrateBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("migrate checkpoint mutation database: %v", err)
	}
	return NewReviewCheckpointStore(db), faults
}

func reviewCheckpointMutationInput(
	key, beadID string,
	state ReviewCheckpointState,
	opsRunID int64,
) CheckpointInput {
	return CheckpointInput{
		CheckpointKey: key, BeadID: beadID, OriginAssignmentID: 41,
		CurrentAssignmentID: 43, WorkerID: "mutation-worker", Worktree: "/tmp/" + key,
		Branch: "agent/" + key, TargetBranch: "main", HeadSHA: "head-" + key,
		TargetSHA: "target-" + key, AcceptanceHash: "acceptance-" + key,
		QGScriptHash: "script-" + key, QGMode: "full", ReviewPolicyHash: "policy-" + key,
		TriageRevision: "triage-" + key, ReadyAttempt: "ready-" + key,
		OpsRunID: opsRunID, State: state,
	}
}

func seedReviewCheckpointMutation(
	t *testing.T,
	store *ReviewCheckpointStore,
	in CheckpointInput,
) ReviewCheckpoint {
	t.Helper()
	checkpoint, err := store.CreateOrReuse(t.Context(), in)
	if err != nil {
		t.Fatalf("seed review checkpoint %s: %v", in.CheckpointKey, err)
	}
	return checkpoint
}

func requireReviewCheckpointMutationError(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestReviewCheckpointMutationOwnershipLoads(t *testing.T) {
	ctx := t.Context()

	t.Run("owning guards reject nil store and empty bead", func(t *testing.T) {
		var nilStore *ReviewCheckpointStore
		if checkpoint, err := nilStore.LoadOwningForBead(ctx, "bead"); err == nil || checkpoint != nil {
			t.Fatalf("nil store result = %#v, %v", checkpoint, err)
		}
		if checkpoint, err := (&ReviewCheckpointStore{}).LoadOwningForBead(ctx, "bead"); err == nil || checkpoint != nil {
			t.Fatalf("nil DB result = %#v, %v", checkpoint, err)
		}
		store, _ := openReviewCheckpointMutationStore(t)
		if checkpoint, err := store.LoadOwningForBead(ctx, ""); err == nil || checkpoint != nil {
			t.Fatalf("empty bead result = %#v, %v", checkpoint, err)
		}
	})

	t.Run("owning returns absent single and rejects ambiguity", func(t *testing.T) {
		store, _ := openReviewCheckpointMutationStore(t)
		checkpoint, err := store.LoadOwningForBead(ctx, "absent")
		if err != nil || checkpoint != nil {
			t.Fatalf("absent owning checkpoint = %#v, %v", checkpoint, err)
		}
		first := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("owning-first", "owned-bead", ReviewCheckpointStateReviewRunning, 0))
		checkpoint, err = store.LoadOwningForBead(ctx, "owned-bead")
		if err != nil || checkpoint == nil || checkpoint.ID != first.ID {
			t.Fatalf("single owning checkpoint = %#v, %v, want ID %d", checkpoint, err, first.ID)
		}
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("owning-second", "owned-bead", ReviewCheckpointStateBlocked, 0))
		checkpoint, err = store.LoadOwningForBead(ctx, "owned-bead")
		if checkpoint != nil || !errors.Is(err, ErrCheckpointOwnershipAmbiguous) {
			t.Fatalf("ambiguous owning checkpoint = %#v, %v", checkpoint, err)
		}
	})

	t.Run("owning propagates count and scan failures", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "SELECT COUNT(*) FROM review_checkpoints"
			f.queryErr = errCheckpointQuery
		})
		_, err := store.LoadOwningForBead(ctx, "bead")
		requireReviewCheckpointMutationError(t, err, errCheckpointQuery)

		store, faults = openReviewCheckpointMutationStore(t)
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("owning-scan", "scan-bead", ReviewCheckpointStateReviewRunning, 0))
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.scanNeedle = "SELECT id, checkpoint_key"
		})
		_, err = store.LoadOwningForBead(ctx, "scan-bead")
		if err == nil || !strings.Contains(err.Error(), "scan review checkpoint") {
			t.Fatalf("owning scan error = %v", err)
		}
	})

	t.Run("ops-run guards exact identity and storage failures", func(t *testing.T) {
		var nilStore *ReviewCheckpointStore
		if checkpoint, err := nilStore.LoadForOpsRun(ctx, 1, "bead"); err == nil || checkpoint != nil {
			t.Fatalf("nil store result = %#v, %v", checkpoint, err)
		}
		if checkpoint, err := (&ReviewCheckpointStore{}).LoadForOpsRun(ctx, 1, "bead"); err == nil || checkpoint != nil {
			t.Fatalf("nil DB result = %#v, %v", checkpoint, err)
		}
		store, _ := openReviewCheckpointMutationStore(t)
		for _, identity := range []struct {
			opsRunID int64
			beadID   string
		}{{0, "bead"}, {1, ""}} {
			checkpoint, err := store.LoadForOpsRun(ctx, identity.opsRunID, identity.beadID)
			if err == nil || checkpoint != nil {
				t.Fatalf("invalid identity %d/%q = %#v, %v", identity.opsRunID, identity.beadID, checkpoint, err)
			}
		}
		checkpoint, err := store.LoadForOpsRun(ctx, 99, "absent")
		if err != nil || checkpoint != nil {
			t.Fatalf("absent ops checkpoint = %#v, %v", checkpoint, err)
		}

		store, faults := openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "FROM review_checkpoints\nWHERE ops_run_id=?"
			f.queryErr = errCheckpointQuery
		})
		_, err = store.LoadForOpsRun(ctx, 91, "bead")
		requireReviewCheckpointMutationError(t, err, errCheckpointQuery)
	})

	t.Run("ops-run returns exact row and rejects cross-bead identity", func(t *testing.T) {
		store, _ := openReviewCheckpointMutationStore(t)
		seeded := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("ops-exact", "ops-bead", ReviewCheckpointStateReviewRunning, 501))
		checkpoint, err := store.LoadForOpsRun(ctx, 501, "ops-bead")
		if err != nil || checkpoint == nil || !reflect.DeepEqual(*checkpoint, seeded) {
			t.Fatalf("exact ops checkpoint = %#v, %v, want %#v", checkpoint, err, seeded)
		}
		checkpoint, err = store.LoadForOpsRun(ctx, 501, "other-bead")
		if checkpoint != nil || !errors.Is(err, ErrCheckpointOwnershipCorrupt) {
			t.Fatalf("cross-bead ops checkpoint = %#v, %v", checkpoint, err)
		}
	})
}

func TestReviewCheckpointMutationLegacyBinding(t *testing.T) {
	ctx := t.Context()

	t.Run("serialized begin propagates begin and write failures and releases connection", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) { f.beginErr = errCheckpointBegin })
		if tx, err := store.beginSerializedOwnershipBind(ctx); tx != nil || !errors.Is(err, errCheckpointBegin) {
			t.Fatalf("injected begin = %#v, %v", tx, err)
		}

		store, faults = openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.execNeedle = "UPDATE review_checkpoints SET updated_at=updated_at WHERE 0"
			f.execErr = errCheckpointExec
		})
		if tx, err := store.beginSerializedOwnershipBind(ctx); tx != nil || !errors.Is(err, errCheckpointExec) {
			t.Fatalf("injected serialization = %#v, %v", tx, err)
		}
		faults.clear()
		pingCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		if err := store.db.PingContext(pingCtx); err != nil {
			t.Fatalf("serialization failure leaked transaction: %v", err)
		}
	})

	t.Run("transactional exact load handles absent query and corrupt identity", func(t *testing.T) {
		store, _ := openReviewCheckpointMutationStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin absent load: %v", err)
		}
		checkpoint, err := loadCheckpointForOpsRunTx(ctx, tx, 701, "absent")
		if err != nil || checkpoint != nil {
			t.Fatalf("absent transactional checkpoint = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()

		store, faults := openReviewCheckpointMutationStore(t)
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin query failure: %v", err)
		}
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "FROM review_checkpoints\nWHERE ops_run_id=?"
			f.queryErr = errCheckpointQuery
		})
		checkpoint, err = loadCheckpointForOpsRunTx(ctx, tx, 702, "bead")
		if checkpoint != nil || !errors.Is(err, errCheckpointQuery) {
			t.Fatalf("transactional query failure = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()

		store, _ = openReviewCheckpointMutationStore(t)
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("load-tx-corrupt", "actual-bead", ReviewCheckpointStateReviewRunning, 703))
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin corrupt load: %v", err)
		}
		checkpoint, err = loadCheckpointForOpsRunTx(ctx, tx, 703, "other-bead")
		if checkpoint != nil || !errors.Is(err, ErrCheckpointOwnershipCorrupt) {
			t.Fatalf("transactional corrupt identity = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()
	})

	t.Run("legacy ID query returns ordered IDs and closes scan failures", func(t *testing.T) {
		store, _ := openReviewCheckpointMutationStore(t)
		first := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("legacy-id-first", "legacy-ids", ReviewCheckpointStateReviewRunning, 0))
		second := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("legacy-id-second", "legacy-ids", ReviewCheckpointStateBlocked, 0))
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin legacy ID query: %v", err)
		}
		ids, err := legacyUnlinkedCheckpointIDs(ctx, tx, "legacy-ids")
		if err != nil || !reflect.DeepEqual(ids, []int64{first.ID, second.ID}) {
			t.Fatalf("legacy IDs = %v, %v", ids, err)
		}
		_ = tx.Rollback()

		store, faults := openReviewCheckpointMutationStore(t)
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("legacy-scan", "legacy-scan", ReviewCheckpointStateReviewRunning, 0))
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin legacy scan: %v", err)
		}
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.scanNeedle = "SELECT id FROM review_checkpoints"
			f.closeNeedle = "SELECT id FROM review_checkpoints"
		})
		ids, err = legacyUnlinkedCheckpointIDs(ctx, tx, "legacy-scan")
		if err == nil || ids != nil || faults.closeCalls() == 0 {
			t.Fatalf("legacy scan failure = %v, %v, close calls %d", ids, err, faults.closeCalls())
		}
		_ = tx.Rollback()
	})

	t.Run("legacy ID query propagates query iteration and close failures", func(t *testing.T) {
		for _, failure := range []struct {
			name      string
			configure func(*reviewCheckpointMutationFaults)
			want      error
		}{
			{"query", func(f *reviewCheckpointMutationFaults) {
				f.queryNeedle = "SELECT id FROM review_checkpoints"
				f.queryErr = errCheckpointQuery
			}, errCheckpointQuery},
			{"iterate", func(f *reviewCheckpointMutationFaults) {
				f.nextNeedle = "SELECT id FROM review_checkpoints"
			}, errCheckpointRows},
			{"close", func(f *reviewCheckpointMutationFaults) {
				f.closeNeedle = "SELECT id FROM review_checkpoints"
				f.closeErr = errCheckpointRows
			}, errCheckpointRows},
		} {
			t.Run(failure.name, func(t *testing.T) {
				store, faults := openReviewCheckpointMutationStore(t)
				seedReviewCheckpointMutation(t, store,
					reviewCheckpointMutationInput("legacy-"+failure.name, "legacy-error", ReviewCheckpointStateReviewRunning, 0))
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin legacy %s: %v", failure.name, err)
				}
				faults.configure(failure.configure)
				ids, err := legacyUnlinkedCheckpointIDs(ctx, tx, "legacy-error")
				if ids != nil || !errors.Is(err, failure.want) {
					t.Fatalf("legacy %s failure = %v, %v", failure.name, ids, err)
				}
				_ = tx.Rollback()
			})
		}
	})

	t.Run("legacy ownership rejects ambiguity and binds exactly one row", func(t *testing.T) {
		store, _ := openReviewCheckpointMutationStore(t)
		first := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("bind-ambiguous-a", "bind-ambiguous", ReviewCheckpointStateReviewRunning, 0))
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("bind-ambiguous-b", "bind-ambiguous", ReviewCheckpointStateBlocked, 0))
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin ambiguous bind: %v", err)
		}
		checkpoint, err := store.bindLegacyCheckpointOwnership(ctx, tx, 711, "bind-ambiguous")
		if checkpoint != nil || !errors.Is(err, ErrCheckpointOwnershipAmbiguous) ||
			!strings.Contains(err.Error(), "found 2 unlinked checkpoints") {
			t.Fatalf("ambiguous legacy bind = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()

		store, _ = openReviewCheckpointMutationStore(t)
		first = seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("bind-single", "bind-single", ReviewCheckpointStateReviewRunning, 0))
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin single bind: %v", err)
		}
		checkpoint, err = store.bindLegacyCheckpointOwnership(ctx, tx, 712, "bind-single")
		if err != nil || checkpoint == nil || checkpoint.ID != first.ID || checkpoint.OpsRunID != 712 {
			t.Fatalf("single legacy bind = %#v, %v", checkpoint, err)
		}
	})

	t.Run("legacy ownership propagates ID query failure", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin failed bind: %v", err)
		}
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "SELECT id FROM review_checkpoints"
			f.queryErr = errCheckpointQuery
		})
		checkpoint, err := store.bindLegacyCheckpointOwnership(ctx, tx, 713, "bind-error")
		if checkpoint != nil || !errors.Is(err, errCheckpointQuery) {
			t.Fatalf("legacy ID query failure = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()
	})

	t.Run("single legacy bind propagates update conflict and commit failures", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		checkpoint := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("bind-exec", "bind-exec", ReviewCheckpointStateReviewRunning, 0))
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin exec bind: %v", err)
		}
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.execNeedle = "UPDATE review_checkpoints SET ops_run_id=?"
			f.execErr = errCheckpointExec
		})
		got, err := store.bindSingleLegacyCheckpoint(ctx, tx, 721, "bind-exec", checkpoint.ID)
		if got != nil || !errors.Is(err, errCheckpointExec) {
			t.Fatalf("bind exec failure = %#v, %v", got, err)
		}
		_ = tx.Rollback()

		store, _ = openReviewCheckpointMutationStore(t)
		checkpoint = seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("bind-conflict", "bind-conflict", ReviewCheckpointStateReviewRunning, 722))
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin conflict bind: %v", err)
		}
		got, err = store.bindSingleLegacyCheckpoint(ctx, tx, 723, "bind-conflict", checkpoint.ID)
		if got != nil || !errors.Is(err, ErrCheckpointConflict) {
			t.Fatalf("bind conflict = %#v, %v", got, err)
		}
		_ = tx.Rollback()

		store, faults = openReviewCheckpointMutationStore(t)
		checkpoint = seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("bind-commit", "bind-commit", ReviewCheckpointStateReviewRunning, 0))
		faults.configure(func(f *reviewCheckpointMutationFaults) { f.commitErr = errCheckpointCommit })
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin commit bind: %v", err)
		}
		got, err = store.bindSingleLegacyCheckpoint(ctx, tx, 724, "bind-commit", checkpoint.ID)
		if got != nil || !errors.Is(err, errCheckpointCommit) {
			t.Fatalf("bind commit failure = %#v, %v", got, err)
		}
	})

	t.Run("absent legacy ownership commits only when no checkpoint owns bead", func(t *testing.T) {
		store, _ := openReviewCheckpointMutationStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin absent ownership: %v", err)
		}
		checkpoint, err := commitAbsentLegacyCheckpointOwnership(ctx, tx, 731, "absent")
		if err != nil || checkpoint != nil {
			t.Fatalf("absent ownership = %#v, %v", checkpoint, err)
		}

		store, _ = openReviewCheckpointMutationStore(t)
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("absent-linked", "linked-bead", ReviewCheckpointStateReviewRunning, 732))
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin linked ownership: %v", err)
		}
		checkpoint, err = commitAbsentLegacyCheckpointOwnership(ctx, tx, 733, "linked-bead")
		if checkpoint != nil || !errors.Is(err, ErrCheckpointOwnershipAmbiguous) {
			t.Fatalf("linked-other ownership = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()
	})

	t.Run("absent legacy ownership propagates count and commit failures", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin absent count: %v", err)
		}
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "SELECT COUNT(*) FROM review_checkpoints"
			f.queryErr = errCheckpointQuery
		})
		checkpoint, err := commitAbsentLegacyCheckpointOwnership(ctx, tx, 734, "absent")
		if checkpoint != nil || !errors.Is(err, errCheckpointQuery) {
			t.Fatalf("absent count failure = %#v, %v", checkpoint, err)
		}
		_ = tx.Rollback()

		store, faults = openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) { f.commitErr = errCheckpointCommit })
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin absent commit: %v", err)
		}
		checkpoint, err = commitAbsentLegacyCheckpointOwnership(ctx, tx, 735, "absent")
		if checkpoint != nil || !errors.Is(err, errCheckpointCommit) {
			t.Fatalf("absent commit failure = %#v, %v", checkpoint, err)
		}
	})

	t.Run("or-bind returns exact rows before beginning fallback", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		seeded := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("or-bind-exact", "or-bind-exact", ReviewCheckpointStateReviewRunning, 741))
		faults.configure(func(f *reviewCheckpointMutationFaults) { f.beginErr = errCheckpointBegin })
		checkpoint, err := store.LoadForOpsRunOrBindLegacy(ctx, 741, "or-bind-exact")
		if err != nil || checkpoint == nil || checkpoint.ID != seeded.ID {
			t.Fatalf("or-bind exact = %#v, %v", checkpoint, err)
		}
	})

	t.Run("or-bind preserves initial and fallback failures", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "FROM review_checkpoints\nWHERE ops_run_id=?"
			f.queryErr = errCheckpointQuery
			f.beginErr = errCheckpointBegin
		})
		checkpoint, err := store.LoadForOpsRunOrBindLegacy(ctx, 742, "or-bind-load-error")
		if checkpoint != nil || !errors.Is(err, errCheckpointQuery) {
			t.Fatalf("or-bind initial failure = %#v, %v", checkpoint, err)
		}

		store, faults = openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) { f.beginErr = errCheckpointBegin })
		checkpoint, err = store.LoadForOpsRunOrBindLegacy(ctx, 743, "or-bind-begin-error")
		if checkpoint != nil || !errors.Is(err, errCheckpointBegin) {
			t.Fatalf("or-bind begin failure = %#v, %v", checkpoint, err)
		}

		store, faults = openReviewCheckpointMutationStore(t)
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.queryNeedle = "FROM review_checkpoints\nWHERE ops_run_id=?"
			f.queryAt = 2
			f.queryErr = errCheckpointQuery
		})
		checkpoint, err = store.LoadForOpsRunOrBindLegacy(ctx, 744, "or-bind-recheck-error")
		if checkpoint != nil || !errors.Is(err, errCheckpointQuery) {
			t.Fatalf("or-bind recheck failure = %#v, %v", checkpoint, err)
		}
		faults.clear()
		pingCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		if err := store.db.PingContext(pingCtx); err != nil {
			t.Fatalf("or-bind recheck leaked transaction: %v", err)
		}
	})

	t.Run("or-bind rechecks an exact link before legacy fallback", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		seeded := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("or-bind-race", "or-bind-race", ReviewCheckpointStateReviewRunning, 0))
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.beforeBegin = func(execer driver.ExecerContext) error {
				_, err := execer.ExecContext(ctx,
					`UPDATE review_checkpoints SET ops_run_id=? WHERE id=?`,
					[]driver.NamedValue{{Ordinal: 1, Value: int64(745)}, {Ordinal: 2, Value: seeded.ID}})
				return err
			}
		})
		checkpoint, err := store.LoadForOpsRunOrBindLegacy(ctx, 745, "or-bind-race")
		if err != nil || checkpoint == nil || checkpoint.ID != seeded.ID || checkpoint.OpsRunID != 745 {
			t.Fatalf("or-bind rechecked exact = %#v, %v", checkpoint, err)
		}
	})

	t.Run("or-bind propagates exact recheck commit failure", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		seeded := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("or-bind-commit", "or-bind-commit", ReviewCheckpointStateReviewRunning, 0))
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.beforeBegin = func(execer driver.ExecerContext) error {
				_, err := execer.ExecContext(ctx,
					`UPDATE review_checkpoints SET ops_run_id=? WHERE id=?`,
					[]driver.NamedValue{{Ordinal: 1, Value: int64(746)}, {Ordinal: 2, Value: seeded.ID}})
				return err
			}
			f.commitErr = errCheckpointCommit
		})
		checkpoint, err := store.LoadForOpsRunOrBindLegacy(ctx, 746, "or-bind-commit")
		if checkpoint != nil || !errors.Is(err, errCheckpointCommit) {
			t.Fatalf("or-bind exact commit failure = %#v, %v", checkpoint, err)
		}
	})
}

func TestReviewCheckpointMutationIntegrationDurability(t *testing.T) {
	ctx := t.Context()

	t.Run("pending list validates store and returns exact ordered rows", func(t *testing.T) {
		var nilStore *ReviewCheckpointStore
		if checkpoints, err := nilStore.ListPendingIntegrations(ctx); err == nil || checkpoints != nil {
			t.Fatalf("nil store pending = %#v, %v", checkpoints, err)
		}
		if checkpoints, err := (&ReviewCheckpointStore{}).ListPendingIntegrations(ctx); err == nil || checkpoints != nil {
			t.Fatalf("nil DB pending = %#v, %v", checkpoints, err)
		}

		store, _ := openReviewCheckpointMutationStore(t)
		first := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("pending-approved", "pending-a", ReviewCheckpointStateApproved, 601))
		second := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("pending-manual", "pending-b", ReviewCheckpointStateManualIntegrationPending, 602))
		third := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("pending-integrating", "pending-c", ReviewCheckpointStateIntegrating, 603))
		seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("pending-blocked", "pending-d", ReviewCheckpointStateBlocked, 604))
		if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET integration_target_before_sha='target-before', integration_approved_head_sha='approved-head',
    integration_observed_target_sha='observed-target', integration_step='merge_observed'
WHERE id=?`, third.ID); err != nil {
			t.Fatalf("seed integration proof: %v", err)
		}
		checkpoints, err := store.ListPendingIntegrations(ctx)
		if err != nil {
			t.Fatalf("list pending integrations: %v", err)
		}
		if len(checkpoints) != 3 || checkpoints[0].ID != first.ID || checkpoints[1].ID != second.ID || checkpoints[2].ID != third.ID {
			t.Fatalf("pending integrations = %#v", checkpoints)
		}
		got := checkpoints[2]
		if got.IntegrationTargetBeforeSHA != "target-before" || got.IntegrationApprovedHeadSHA != "approved-head" ||
			got.IntegrationObservedTargetSHA != "observed-target" || got.IntegrationStep != "merge_observed" {
			t.Fatalf("pending durable proof = %#v", got)
		}
	})

	t.Run("pending list propagates query scan and iteration failures", func(t *testing.T) {
		for _, failure := range []struct {
			name      string
			configure func(*reviewCheckpointMutationFaults)
			want      error
		}{
			{"query", func(f *reviewCheckpointMutationFaults) {
				f.queryNeedle = "WHERE state IN ('approved'"
				f.queryErr = errCheckpointQuery
			}, errCheckpointQuery},
			{"scan", func(f *reviewCheckpointMutationFaults) {
				f.scanNeedle = "WHERE state IN ('approved'"
			}, nil},
			{"iterate", func(f *reviewCheckpointMutationFaults) {
				f.nextNeedle = "WHERE state IN ('approved'"
			}, errCheckpointRows},
		} {
			t.Run(failure.name, func(t *testing.T) {
				store, faults := openReviewCheckpointMutationStore(t)
				seedReviewCheckpointMutation(t, store,
					reviewCheckpointMutationInput("pending-error-"+failure.name, "pending-error", ReviewCheckpointStateApproved, 610))
				faults.configure(failure.configure)
				checkpoints, err := store.ListPendingIntegrations(ctx)
				if err == nil || checkpoints != nil {
					t.Fatalf("%s pending failure = %#v, %v", failure.name, checkpoints, err)
				}
				if failure.want != nil {
					requireReviewCheckpointMutationError(t, err, failure.want)
				}
			})
		}
	})

	t.Run("begin integration validates identity and persists exact CAS intent", func(t *testing.T) {
		var nilStore *ReviewCheckpointStore
		if err := nilStore.BeginIntegration(ctx, 1, ReviewCheckpointStateApproved, ReviewCheckpointStateIntegrating, "target", "head"); err == nil {
			t.Fatal("nil store begin integration succeeded")
		}
		if err := (&ReviewCheckpointStore{}).BeginIntegration(ctx, 1, ReviewCheckpointStateApproved, ReviewCheckpointStateIntegrating, "target", "head"); err == nil {
			t.Fatal("nil DB begin integration succeeded")
		}
		store, _ := openReviewCheckpointMutationStore(t)
		for _, input := range []struct {
			id           int64
			target, head string
		}{{0, "target", "head"}, {1, "", "head"}, {1, "target", ""}} {
			err := store.BeginIntegration(ctx, input.id, ReviewCheckpointStateApproved, ReviewCheckpointStateIntegrating, input.target, input.head)
			if err == nil || !strings.Contains(err.Error(), "missing required identity") {
				t.Fatalf("invalid begin identity %+v error = %v", input, err)
			}
		}

		checkpoint := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("begin-intent", "begin-bead", ReviewCheckpointStateApproved, 620))
		if err := store.BeginIntegration(ctx, checkpoint.ID, ReviewCheckpointStateApproved, ReviewCheckpointStateIntegrating, "target-before", "approved-head"); err != nil {
			t.Fatalf("begin integration: %v", err)
		}
		var state ReviewCheckpointState
		var target, head, step string
		if err := store.db.QueryRowContext(ctx, `
SELECT state, integration_target_before_sha, integration_approved_head_sha, integration_step
FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&state, &target, &head, &step); err != nil {
			t.Fatalf("read integration intent: %v", err)
		}
		if state != ReviewCheckpointStateIntegrating || target != "target-before" || head != "approved-head" || step != "intent" {
			t.Fatalf("integration intent = %q/%q/%q/%q", state, target, head, step)
		}
		if err := store.BeginIntegration(ctx, checkpoint.ID, ReviewCheckpointStateApproved, ReviewCheckpointStateIntegrating, "other", "other"); !errors.Is(err, ErrCheckpointConflict) {
			t.Fatalf("stale begin error = %v", err)
		}
	})

	t.Run("begin integration propagates write failure", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		checkpoint := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("begin-write", "begin-write-bead", ReviewCheckpointStateApproved, 621))
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.execNeedle = "UPDATE review_checkpoints\nSET state=?"
			f.execErr = errCheckpointExec
		})
		err := store.BeginIntegration(ctx, checkpoint.ID, ReviewCheckpointStateApproved, ReviewCheckpointStateIntegrating, "target", "head")
		requireReviewCheckpointMutationError(t, err, errCheckpointExec)
	})

	t.Run("block integration persists one durable condition", func(t *testing.T) {
		var nilStore *ReviewCheckpointStore
		if changed, err := nilStore.BlockIntegration(ctx, 1, "reason"); err == nil || changed {
			t.Fatalf("nil store block = %v, %v", changed, err)
		}
		if changed, err := (&ReviewCheckpointStore{}).BlockIntegration(ctx, 1, "reason"); err == nil || changed {
			t.Fatalf("nil DB block = %v, %v", changed, err)
		}
		store, _ := openReviewCheckpointMutationStore(t)
		checkpoint := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("block-int", "block-bead", ReviewCheckpointStateApproved, 630))
		changed, err := store.BlockIntegration(ctx, checkpoint.ID, "branch diverged")
		if err != nil || !changed {
			t.Fatalf("first block = %v, %v", changed, err)
		}
		var state ReviewCheckpointState
		var step, summary, blockers string
		if err := store.db.QueryRowContext(ctx, `
SELECT state, integration_step, summary, blockers_json FROM review_checkpoints WHERE id=?`, checkpoint.ID).
			Scan(&state, &step, &summary, &blockers); err != nil {
			t.Fatalf("read blocked checkpoint: %v", err)
		}
		if state != ReviewCheckpointStateBlocked || step != "blocked" || summary != "branch diverged" || blockers != `["branch diverged"]` {
			t.Fatalf("blocked checkpoint = %q/%q/%q/%q", state, step, summary, blockers)
		}
		changed, err = store.BlockIntegration(ctx, checkpoint.ID, "duplicate")
		if err != nil || changed {
			t.Fatalf("repeated block = %v, %v", changed, err)
		}
	})

	t.Run("block integration propagates exec and result failures", func(t *testing.T) {
		store, faults := openReviewCheckpointMutationStore(t)
		checkpoint := seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("block-exec", "block-exec-bead", ReviewCheckpointStateApproved, 631))
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.execNeedle = "SET state='blocked'"
			f.execErr = errCheckpointExec
		})
		_, err := store.BlockIntegration(ctx, checkpoint.ID, "reason")
		requireReviewCheckpointMutationError(t, err, errCheckpointExec)

		store, faults = openReviewCheckpointMutationStore(t)
		checkpoint = seedReviewCheckpointMutation(t, store,
			reviewCheckpointMutationInput("block-result", "block-result-bead", ReviewCheckpointStateApproved, 632))
		faults.configure(func(f *reviewCheckpointMutationFaults) {
			f.resultNeedle = "SET state='blocked'"
			f.rowsAffectedErr = errCheckpointRowsAffected
		})
		_, err = store.BlockIntegration(ctx, checkpoint.ID, "reason")
		requireReviewCheckpointMutationError(t, err, errCheckpointRowsAffected)
	})
}
