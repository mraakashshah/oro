package dispatcher

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
)

var (
	errBufferAdmissionExec = errors.New("injected admission exec failure")
	errBufferPrepare       = errors.New("buffer admission driver does not prepare statements")
	errBufferBegin         = errors.New("buffer admission driver does not use driver transactions")
)

type bufferAdmissionDriverState struct {
	mu        sync.Mutex
	failQuery string
	queries   []string
}

func (s *bufferAdmissionDriverState) failOn(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failQuery = query
}

func (s *bufferAdmissionDriverState) clearFailure() {
	s.failOn("")
}

func (s *bufferAdmissionDriverState) count(query string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, observed := range s.queries {
		if strings.Contains(observed, query) {
			count++
		}
	}
	return count
}

type bufferAdmissionConnector struct {
	state *bufferAdmissionDriverState
}

func (c *bufferAdmissionConnector) Connect(context.Context) (driver.Conn, error) {
	return &bufferAdmissionConn{state: c.state}, nil
}

func (c *bufferAdmissionConnector) Driver() driver.Driver {
	return &bufferAdmissionDriver{state: c.state}
}

type bufferAdmissionDriver struct {
	state *bufferAdmissionDriverState
}

func (d *bufferAdmissionDriver) Open(string) (driver.Conn, error) {
	return &bufferAdmissionConn{state: d.state}, nil
}

type bufferAdmissionConn struct {
	state *bufferAdmissionDriverState
}

func (c *bufferAdmissionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errBufferPrepare
}

func (c *bufferAdmissionConn) Close() error {
	return nil
}

func (c *bufferAdmissionConn) Begin() (driver.Tx, error) {
	return nil, errBufferBegin
}

func (c *bufferAdmissionConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)
	if c.state.failQuery != "" && strings.Contains(query, c.state.failQuery) {
		return nil, errBufferAdmissionExec
	}
	return driver.RowsAffected(1), nil
}

func (*bufferAdmissionConn) CheckNamedValue(*driver.NamedValue) error {
	return nil
}

func (*bufferAdmissionConn) Ping(context.Context) error {
	return nil
}

func newBufferAdmissionDispatcher(t *testing.T) (*Dispatcher, *bufferAdmissionDriverState) {
	t.Helper()
	state := &bufferAdmissionDriverState{}
	db := sql.OpenDB(&bufferAdmissionConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &Dispatcher{db: db}, state
}

func requireBufferAdmissionMutexReleased(t *testing.T, d *Dispatcher) {
	t.Helper()
	if !d.assignmentAdmissionMu.TryLock() {
		t.Fatal("assignment admission mutex remains locked")
	}
	d.assignmentAdmissionMu.Unlock()
}

func TestBufferAssignmentAdmissionBeginOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("nil database closes admission", func(t *testing.T) {
		d := &Dispatcher{}
		admission, err := d.beginAssignmentAdmission(ctx, "nil-db")
		if admission != nil || err == nil || !strings.Contains(err.Error(), "nil-db assignment admission: database is nil") {
			t.Fatalf("begin with nil database = %#v, %v", admission, err)
		}
		requireBufferAdmissionMutexReleased(t, d)
	})

	t.Run("closed database releases mutex", func(t *testing.T) {
		d, _ := newBufferAdmissionDispatcher(t)
		if err := d.db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		admission, err := d.beginAssignmentAdmission(ctx, "closed-db")
		if admission != nil || err == nil || !strings.Contains(err.Error(), "closed-db assignment admission: open connection") {
			t.Fatalf("begin with closed database = %#v, %v", admission, err)
		}
		requireBufferAdmissionMutexReleased(t, d)
	})

	for _, tc := range []struct {
		name      string
		failQuery string
		wantError string
	}{
		{name: "busy timeout failure", failQuery: "PRAGMA busy_timeout", wantError: "set busy timeout"},
		{name: "begin failure", failQuery: "BEGIN IMMEDIATE", wantError: "begin immediate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, state := newBufferAdmissionDispatcher(t)
			state.failOn(tc.failQuery)
			admission, err := d.beginAssignmentAdmission(ctx, "fault")
			if admission != nil || !errors.Is(err, errBufferAdmissionExec) || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("begin with %s failure = %#v, %v", tc.failQuery, admission, err)
			}
			if state.count("ROLLBACK") != 1 {
				t.Fatalf("rollback count = %d, want 1", state.count("ROLLBACK"))
			}
			requireBufferAdmissionMutexReleased(t, d)
		})
	}

	t.Run("success opens exact transaction", func(t *testing.T) {
		d, state := newBufferAdmissionDispatcher(t)
		admission, err := d.beginAssignmentAdmission(ctx, "success")
		if err != nil || admission == nil || admission.d != d || admission.conn == nil || admission.closed || admission.committed {
			t.Fatalf("successful admission = %#v, %v", admission, err)
		}
		if state.count("PRAGMA busy_timeout=5000") != 1 || state.count("BEGIN IMMEDIATE") != 1 {
			t.Fatalf("transaction setup queries = %#v", state.queries)
		}
		admission.close()
	})
}

func TestBufferAssignmentAdmissionCommitOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid admissions fail closed", func(t *testing.T) {
		var nilAdmission *assignmentAdmission
		if err := nilAdmission.commit(ctx, "nil"); err == nil || !strings.Contains(err.Error(), "transaction is not open") {
			t.Fatalf("nil admission commit error = %v", err)
		}
		if err := (&assignmentAdmission{}).commit(ctx, "no-conn"); err == nil || !strings.Contains(err.Error(), "transaction is not open") {
			t.Fatalf("connectionless admission commit error = %v", err)
		}

		d, _ := newBufferAdmissionDispatcher(t)
		admission, err := d.beginAssignmentAdmission(ctx, "closed")
		if err != nil {
			t.Fatalf("begin closed admission: %v", err)
		}
		admission.closed = true
		if err := admission.commit(ctx, "closed"); err == nil || !strings.Contains(err.Error(), "transaction is not open") {
			t.Fatalf("closed admission commit error = %v", err)
		}
		admission.closed = false
		admission.close()
	})

	t.Run("success marks committed and suppresses rollback", func(t *testing.T) {
		d, state := newBufferAdmissionDispatcher(t)
		admission, err := d.beginAssignmentAdmission(ctx, "success")
		if err != nil {
			t.Fatalf("begin admission: %v", err)
		}
		if err := admission.commit(ctx, "success"); err != nil {
			t.Fatalf("commit admission: %v", err)
		}
		if !admission.committed || state.count("COMMIT") != 1 {
			t.Fatalf("committed/query count = %v/%d", admission.committed, state.count("COMMIT"))
		}
		admission.close()
		if state.count("ROLLBACK") != 0 {
			t.Fatalf("committed admission rollback count = %d", state.count("ROLLBACK"))
		}
	})

	t.Run("commit failure rolls back and remains uncommitted", func(t *testing.T) {
		d, state := newBufferAdmissionDispatcher(t)
		admission, err := d.beginAssignmentAdmission(ctx, "failure")
		if err != nil {
			t.Fatalf("begin admission: %v", err)
		}
		state.failOn("COMMIT")
		err = admission.commit(ctx, "failure")
		if !errors.Is(err, errBufferAdmissionExec) || !strings.Contains(err.Error(), "failure assignment admission: commit") {
			t.Fatalf("commit failure = %v", err)
		}
		if admission.committed || state.count("ROLLBACK") != 1 {
			t.Fatalf("failed commit state/rollback count = %v/%d", admission.committed, state.count("ROLLBACK"))
		}
		state.clearFailure()
		admission.close()
	})
}

func TestBufferAssignmentAdmissionCloseOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("nil and already closed admissions are inert", func(t *testing.T) {
		var nilAdmission *assignmentAdmission
		nilAdmission.close()

		d := &Dispatcher{}
		d.assignmentAdmissionMu.Lock()
		admission := &assignmentAdmission{d: d, closed: true}
		admission.close()
		if d.assignmentAdmissionMu.TryLock() {
			d.assignmentAdmissionMu.Unlock()
			t.Fatal("already closed admission unlocked mutex twice")
		}
		d.assignmentAdmissionMu.Unlock()
	})

	t.Run("uncommitted close rolls back closes and unlocks", func(t *testing.T) {
		d, state := newBufferAdmissionDispatcher(t)
		admission, err := d.beginAssignmentAdmission(ctx, "close")
		if err != nil {
			t.Fatalf("begin admission: %v", err)
		}
		conn := admission.conn
		admission.close()
		if !admission.closed || state.count("ROLLBACK") != 1 {
			t.Fatalf("closed/rollback count = %v/%d", admission.closed, state.count("ROLLBACK"))
		}
		if err := conn.PingContext(ctx); !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("closed connection ping error = %v, want sql.ErrConnDone", err)
		}
		requireBufferAdmissionMutexReleased(t, d)
	})

	t.Run("connectionless close still marks closed and unlocks", func(t *testing.T) {
		d := &Dispatcher{}
		d.assignmentAdmissionMu.Lock()
		admission := &assignmentAdmission{d: d}
		admission.close()
		if !admission.closed {
			t.Fatal("connectionless admission was not marked closed")
		}
		requireBufferAdmissionMutexReleased(t, d)
	})
}
