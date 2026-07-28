package dispatcher //nolint:testpackage // white-box tests need access to Dispatcher internals

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

var errTestShow = errors.New("bead not found")

// TestShouldRetryEscalation verifies that shouldRetryEscalation returns false
// when the underlying condition is resolved, preventing escalation spam.
func TestShouldRetryEscalation(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		escType     string
		beadID      string
		setupBead   func(*fakeBeadStore)
		setupWorker func(*Dispatcher)
		want        bool
	}{
		{
			name:    "MISSING_AC resolved - AC now populated",
			escType: "MISSING_AC",
			beadID:  "oro-test1",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-test1"] = &protocol.BeadDetail{
					ID:                 "oro-test1",
					AcceptanceCriteria: "Test: foo | Cmd: bar | Assert: baz",
				}
			},
			want: false, // should NOT retry - AC is now present
		},
		{
			name:    "MISSING_AC still missing - no AC",
			escType: "MISSING_AC",
			beadID:  "oro-test2",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-test2"] = &protocol.BeadDetail{
					ID:                 "oro-test2",
					AcceptanceCriteria: "",
				}
			},
			want: true, // should retry - AC still missing
		},
		{
			name:    "MISSING_AC closed bead - do not retry",
			escType: "MISSING_AC",
			beadID:  "oro-closed1",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-closed1"] = &protocol.BeadDetail{
					ID:                 "oro-closed1",
					AcceptanceCriteria: "", // no AC, but bead is closed
					Status:             "closed",
				}
			},
			want: false, // should NOT retry - bead is closed
		},
		{
			name:    "STUCK_WORKER resolved - worker no longer exists",
			escType: "STUCK_WORKER",
			beadID:  "oro-test3",
			setupWorker: func(d *Dispatcher) {
				// Worker does NOT exist in d.workers
			},
			want: false, // should NOT retry - worker gone
		},
		{
			name:    "STUCK_WORKER still exists",
			escType: "STUCK_WORKER",
			beadID:  "oro-test4",
			setupWorker: func(d *Dispatcher) {
				d.workers["worker-1"] = &trackedWorker{
					id:     "worker-1",
					beadID: "oro-test4",
				}
			},
			want: true, // should retry - worker still exists
		},
		{
			name:    "WORKER_CRASH resolved - bead no longer in_progress",
			escType: "WORKER_CRASH",
			beadID:  "oro-test5",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-test5"] = &protocol.BeadDetail{
					ID: "oro-test5",
					// WorkerID is empty (closed or not in_progress)
				}
			},
			want: false, // should NOT retry - bead was re-queued or closed
		},
		{
			name:    "STUCK resolved - bead status changed",
			escType: "STUCK",
			beadID:  "oro-test6",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-test6"] = &protocol.BeadDetail{
					ID: "oro-test6",
					// WorkerID is empty (not in_progress anymore)
				}
			},
			want: false, // should NOT retry - status changed
		},
		{
			name:    "MERGE_CONFLICT resolved - bead closed",
			escType: "MERGE_CONFLICT",
			beadID:  "oro-test7",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-test7"] = &protocol.BeadDetail{
					ID: "oro-test7",
					// WorkerID is empty (closed)
				}
			},
			want: false, // should NOT retry - bead merged and closed
		},
		{
			name:    "PRIORITY_CONTENTION resolved - bead in_progress",
			escType: "PRIORITY_CONTENTION",
			beadID:  "oro-test8",
			setupBead: func(m *fakeBeadStore) {
				if m.shown == nil {
					m.shown = make(map[string]*protocol.BeadDetail)
				}
				m.shown["oro-test8"] = &protocol.BeadDetail{
					ID:       "oro-test8",
					WorkerID: "worker-1", // bead was picked up
				}
			},
			want: false, // should NOT retry - bead was assigned
		},
		{
			name:    "empty beadID STUCK_WORKER - always retry",
			escType: "STUCK_WORKER",
			beadID:  "",
			want:    true, // should retry - no bead context
		},
		{
			name:    "empty beadID WORKER_CRASH - auto-ack stale escalation",
			escType: "WORKER_CRASH",
			beadID:  "",
			want:    false, // should NOT retry - stale crash alert from prev-session worker with no bead (oro-p2ey)
		},
		{
			name:    "beads.Show error - always retry",
			escType: "MISSING_AC",
			beadID:  "oro-nonexistent",
			setupBead: func(m *fakeBeadStore) {
				// Bead does not exist in shown map - Show will return default
			},
			want: false, // should NOT retry - default has AC
		},
		{
			name:    "unknown escalation type - always retry",
			escType: "FUTURE_TYPE",
			beadID:  "oro-test9",
			want:    true, // should retry - don't block future types
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock bead source
			beadSrc := &fakeBeadStore{
				shown: make(map[string]*protocol.BeadDetail),
			}
			if tt.setupBead != nil {
				tt.setupBead(beadSrc)
			}

			// Create minimal dispatcher
			d := &Dispatcher{
				beads:      beadSrc,
				WorkerPool: WorkerPool{workers: make(map[string]*trackedWorker)},
			}
			if tt.setupWorker != nil {
				tt.setupWorker(d)
			}

			got := d.shouldRetryEscalation(ctx, tt.escType, tt.beadID)
			if got != tt.want {
				t.Errorf("shouldRetryEscalation(%q, %q) = %v, want %v",
					tt.escType, tt.beadID, got, tt.want)
			}
		})
	}
}

// TestShouldRetryEscalationOversizedNeverRetries pins the post-gate-removal
// contract: the oversized admission gate is gone, so nothing raises
// OVERSIZED_BEAD any more and every surviving pending row is stale. The
// escalation must therefore never retry — whatever the bead looks like.
//
// The switch case in shouldRetryEscalation must stay and return false. If it
// is deleted instead, control falls through to `default: return true`
// ("unknown escType: always retry") and every pending OVERSIZED_BEAD row
// retries forever without ever reaching a terminal state.
func TestShouldRetryEscalationOversizedNeverRetries(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		beadID    string
		setupBead func(*fakeBeadStore)
	}{
		{
			name:   "open task that would previously look oversized",
			beadID: "oro-big1",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-big1"] = &protocol.BeadDetail{
					ID:                 "oro-big1",
					Type:               "task",
					AcceptanceCriteria: "Read: pkg/ops/foo.go\nRead: pkg/dispatcher/bar.go\nRead: pkg/protocol/baz.go",
				}
			},
		},
		{
			name:   "bead store error no longer forces a retry",
			beadID: "oro-missing",
			setupBead: func(m *fakeBeadStore) {
				m.showErr = errTestShow
			},
		},
		{
			name:   "empty beadID",
			beadID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadSrc := &fakeBeadStore{shown: make(map[string]*protocol.BeadDetail)}
			if tt.setupBead != nil {
				tt.setupBead(beadSrc)
			}

			d := &Dispatcher{
				beads:      beadSrc,
				WorkerPool: WorkerPool{workers: make(map[string]*trackedWorker)},
			}

			if d.shouldRetryEscalation(ctx, string(protocol.EscOversizedBead), tt.beadID) {
				t.Errorf("shouldRetryEscalation(OVERSIZED_BEAD, %q) = true, want false "+
					"(gate removed; escalation is always stale)", tt.beadID)
			}
		})
	}
}

// TestRetryNonTDDAC verifies the NON_TDD_AC escalation precheck:
//
//	(a) open bead with Cmd/Assert but no Test: → retry (still broken)
//	(b) in_progress bead → no retry (worker is handling it)
//	(c) AC gains Test: → no retry (fixed)
//	(d) epic type → no retry (not worker-executable)
//	(e) closed bead → no retry (resolved)
//	(f) bead Show error → retry (don't suppress on error)
func TestRetryNonTDDAC(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		beadID    string
		setupBead func(*fakeBeadStore)
		want      bool
	}{
		{
			name:   "(a) open with Cmd/Assert but no Test: — retry",
			beadID: "oro-a",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-a"] = &protocol.BeadDetail{
					ID:                 "oro-a",
					Status:             "open",
					Type:               "task",
					AcceptanceCriteria: "Cmd: go test ./... | Assert: PASS",
				}
			},
			want: true,
		},
		{
			name:   "(b) in_progress — no retry",
			beadID: "oro-b",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-b"] = &protocol.BeadDetail{
					ID:                 "oro-b",
					Status:             "in_progress",
					Type:               "task",
					AcceptanceCriteria: "Cmd: go test ./... | Assert: PASS",
				}
			},
			want: false,
		},
		{
			name:   "(c) AC gains Test: — no retry",
			beadID: "oro-c",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-c"] = &protocol.BeadDetail{
					ID:                 "oro-c",
					Status:             "open",
					Type:               "task",
					AcceptanceCriteria: "Test: pkg/foo/foo_test.go:TestFoo | Cmd: go test ./... | Assert: PASS",
				}
			},
			want: false,
		},
		{
			name:   "(d) epic type — no retry",
			beadID: "oro-d",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-d"] = &protocol.BeadDetail{
					ID:                 "oro-d",
					Status:             "open",
					Type:               "epic",
					AcceptanceCriteria: "Cmd: go test ./... | Assert: PASS",
				}
			},
			want: false,
		},
		{
			name:   "(e) closed — no retry",
			beadID: "oro-e",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-e"] = &protocol.BeadDetail{
					ID:                 "oro-e",
					Status:             "closed",
					Type:               "task",
					AcceptanceCriteria: "Cmd: go test ./... | Assert: PASS",
				}
			},
			want: false,
		},
		{
			name:   "(f) Show error — retry (don't suppress)",
			beadID: "oro-f",
			setupBead: func(m *fakeBeadStore) {
				m.showErr = errTestShow
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadSrc := &fakeBeadStore{
				shown: make(map[string]*protocol.BeadDetail),
			}
			if tt.setupBead != nil {
				tt.setupBead(beadSrc)
			}

			d := &Dispatcher{
				beads:      beadSrc,
				WorkerPool: WorkerPool{workers: make(map[string]*trackedWorker)},
			}

			got := d.shouldRetryEscalation(ctx, string(protocol.EscNonTDDAC), tt.beadID)
			if got != tt.want {
				t.Errorf("shouldRetryEscalation(NON_TDD_AC, %q) = %v, want %v",
					tt.beadID, got, tt.want)
			}
		})
	}
}

// TestShouldRetryEscalation_ManualIntegration verifies that MANUAL_INTEGRATION
// escalations are never re-delivered by the retry loop. They are one-shot
// notifications: once the escalation fires, the human reads and acts; no
// repeat reminder is needed. Without this case the default branch returns
// true, causing the 50ms test ticker to pump duplicate messages and making
// TestHandleDoneManualIntegrationSkipsMergeAndPreservesWorktree flaky under load.
func TestShouldRetryEscalation_ManualIntegration(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		beadID    string
		setupBead func(*fakeBeadStore)
	}{
		{
			name:   "bead still blocked — no retry",
			beadID: "oro-mi1",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-mi1"] = &protocol.BeadDetail{
					ID:     "oro-mi1",
					Status: "blocked",
				}
			},
		},
		{
			name:   "bead closed (merged) — no retry",
			beadID: "oro-mi2",
			setupBead: func(m *fakeBeadStore) {
				m.shown["oro-mi2"] = &protocol.BeadDetail{
					ID:     "oro-mi2",
					Status: "closed",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadSrc := &fakeBeadStore{shown: make(map[string]*protocol.BeadDetail)}
			if tt.setupBead != nil {
				tt.setupBead(beadSrc)
			}
			d := &Dispatcher{
				beads:      beadSrc,
				WorkerPool: WorkerPool{workers: make(map[string]*trackedWorker)},
			}
			got := d.shouldRetryEscalation(ctx, string(protocol.EscManualIntegration), tt.beadID)
			if got {
				t.Errorf("shouldRetryEscalation(MANUAL_INTEGRATION, %q) = true, want false (one-shot notification must not retry)", tt.beadID)
			}
		})
	}
}

// TestRetryPendingEscalations_AutoAck verifies that retryPendingEscalations
// auto-acks resolved escalations instead of re-sending them.
func TestRetryPendingEscalations_AutoAck(t *testing.T) {
	ctx := context.Background()

	// Setup in-memory database
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create escalations table
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS escalations (
		    id INTEGER PRIMARY KEY,
		    type TEXT NOT NULL,
		    bead_id TEXT,
		    worker_id TEXT,
		    message TEXT NOT NULL,
		    status TEXT NOT NULL DEFAULT 'pending',
		    created_at TEXT NOT NULL DEFAULT (datetime('now')),
		    acked_at TEXT,
		    retry_count INTEGER DEFAULT 0,
		    last_retry_at TEXT
		)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Insert test escalations - one resolved, one unresolved
	_, err = db.ExecContext(ctx, `
		INSERT INTO escalations (type, bead_id, message, status, retry_count)
		VALUES
			('MISSING_AC', 'oro-resolved', '[ORO-DISPATCH] MISSING_AC: oro-resolved', 'pending', 0),
			('MISSING_AC', 'oro-unresolved', '[ORO-DISPATCH] MISSING_AC: oro-unresolved', 'pending', 0)
	`)
	if err != nil {
		t.Fatalf("failed to insert escalations: %v", err)
	}

	// Setup mock bead source
	beadSrc := &fakeBeadStore{
		shown: map[string]*protocol.BeadDetail{
			"oro-resolved": {
				ID:                 "oro-resolved",
				AcceptanceCriteria: "Test: foo | Cmd: bar | Assert: baz", // AC populated
			},
			"oro-unresolved": {
				ID:                 "oro-unresolved",
				AcceptanceCriteria: "", // AC still missing
			},
		},
	}

	// Setup mock escalator to track retries
	escalator := &mockEscalator{}

	// Create dispatcher
	d := &Dispatcher{
		db:        db,
		beads:     beadSrc,
		escalator: escalator,
		WorkerPool: WorkerPool{
			workers: make(map[string]*trackedWorker),
		},
	}

	// Run retry logic
	d.retryPendingEscalations(ctx)

	// Verify: resolved escalation was auto-acked
	var resolvedStatus string
	var resolvedAckedAt sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT status, acked_at FROM escalations WHERE bead_id = 'oro-resolved'`).
		Scan(&resolvedStatus, &resolvedAckedAt)
	if err != nil {
		t.Fatalf("failed to query resolved escalation: %v", err)
	}
	if resolvedStatus != "acked" {
		t.Errorf("resolved escalation status = %q, want 'acked'", resolvedStatus)
	}
	if !resolvedAckedAt.Valid || resolvedAckedAt.String == "" {
		t.Error("resolved escalation acked_at is NULL or empty, want timestamp")
	}

	// Verify: unresolved escalation was retried
	var unresolvedRetryCount int
	err = db.QueryRowContext(ctx,
		`SELECT retry_count FROM escalations WHERE bead_id = 'oro-unresolved'`).
		Scan(&unresolvedRetryCount)
	if err != nil {
		t.Fatalf("failed to query unresolved escalation: %v", err)
	}
	if unresolvedRetryCount != 1 {
		t.Errorf("unresolved escalation retry_count = %d, want 1", unresolvedRetryCount)
	}

	// Verify: only unresolved escalation was sent to escalator
	messages := escalator.Messages()
	if len(messages) != 1 {
		t.Fatalf("escalator called %d times, want 1", len(messages))
	}
	if messages[0] != "[ORO-DISPATCH] MISSING_AC: oro-unresolved" {
		t.Errorf("escalator received %q, want '[ORO-DISPATCH] MISSING_AC: oro-unresolved'", messages[0])
	}
}
