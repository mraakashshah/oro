package dispatcher

import (
	"context"
	"database/sql"
	"fmt"
)

// assignmentAdmission serializes live-assignment creation and claim paths in
// this dispatcher while BEGIN IMMEDIATE excludes out-of-band SQLite writers.
// Callers keep the admission open across any in-memory ownership publication
// or authoritative bead transition that must be atomic with the durable row.
type assignmentAdmission struct {
	d         *Dispatcher
	conn      *sql.Conn
	committed bool
	closed    bool
}

func (d *Dispatcher) beginAssignmentAdmission(ctx context.Context, operation string) (*assignmentAdmission, error) {
	d.assignmentAdmissionMu.Lock()
	admission := &assignmentAdmission{d: d}
	if d.db == nil {
		admission.close()
		return nil, fmt.Errorf("%s assignment admission: database is nil", operation)
	}
	conn, err := d.db.Conn(ctx)
	if err != nil {
		admission.close()
		return nil, fmt.Errorf("%s assignment admission: open connection: %w", operation, err)
	}
	admission.conn = conn
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		admission.close()
		return nil, fmt.Errorf("%s assignment admission: set busy timeout: %w", operation, err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		admission.close()
		return nil, fmt.Errorf("%s assignment admission: begin immediate: %w", operation, err)
	}
	return admission, nil
}

func (a *assignmentAdmission) commit(ctx context.Context, operation string) error {
	if a == nil || a.conn == nil || a.closed {
		return fmt.Errorf("%s assignment admission: transaction is not open", operation)
	}
	if _, err := a.conn.ExecContext(ctx, `COMMIT`); err != nil {
		_, _ = a.conn.ExecContext(context.Background(), `ROLLBACK`)
		return fmt.Errorf("%s assignment admission: commit: %w", operation, err)
	}
	a.committed = true
	return nil
}

func (a *assignmentAdmission) close() {
	if a == nil || a.closed {
		return
	}
	a.closed = true
	if a.conn != nil {
		if !a.committed {
			_, _ = a.conn.ExecContext(context.Background(), `ROLLBACK`)
		}
		_ = a.conn.Close()
	}
	a.d.assignmentAdmissionMu.Unlock()
}
