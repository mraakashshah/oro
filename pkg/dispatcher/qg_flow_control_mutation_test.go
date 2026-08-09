package dispatcher //nolint:testpackage // focused mutation owner exercises private QG flow control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

const (
	qgFlowControlMutationWorkerID  = "flow-control-worker"
	qgFlowControlMutationBeadID    = "flow-control-bead"
	qgFlowControlMutationTargetSHA = "flow-control-target"
	qgFlowControlMutationCandidate = "flow-control-candidate"
	qgFlowControlMutationOutput    = "revive failed: pkg/example.go:12: builtinShadow"
)

type qgFlowControlMutationRunner struct {
	mu     sync.Mutex
	output []byte
	err    error
	calls  []string
}

func (r *qgFlowControlMutationRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return append([]byte(nil), r.output...), r.err
}

func (r *qgFlowControlMutationRunner) setOutput(output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.output = []byte(output)
	r.err = nil
}

type qgFlowControlMutationStore struct {
	*beadstore.FakeStore
	mu    sync.Mutex
	calls []string
}

func newQGFlowControlMutationStore() *qgFlowControlMutationStore {
	return &qgFlowControlMutationStore{FakeStore: beadstore.NewFakeStore()}
}

func (s *qgFlowControlMutationStore) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *qgFlowControlMutationStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	s.record("show:" + id)
	return s.FakeStore.Show(ctx, id)
}

func (s *qgFlowControlMutationStore) Create(
	ctx context.Context,
	params beadstore.CreateParams,
) (*protocol.Bead, error) {
	s.record("create:" + params.ID)
	return s.FakeStore.Create(ctx, params)
}

func (s *qgFlowControlMutationStore) Update(
	ctx context.Context,
	id string,
	params beadstore.UpdateParams,
) error {
	s.record("update:" + id)
	return s.FakeStore.Update(ctx, id, params)
}

func (s *qgFlowControlMutationStore) Defer(ctx context.Context, id, until string) error {
	s.record("defer:" + id)
	return s.FakeStore.Defer(ctx, id, until)
}

func (s *qgFlowControlMutationStore) callsContaining(fragment string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.calls {
		if strings.Contains(call, fragment) {
			count++
		}
	}
	return count
}

type qgFlowControlMutationConn struct {
	mu     sync.Mutex
	writes []byte
}

func (c *qgFlowControlMutationConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *qgFlowControlMutationConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, data...)
	return len(data), nil
}

func (c *qgFlowControlMutationConn) Close() error                     { return nil }
func (c *qgFlowControlMutationConn) LocalAddr() net.Addr              { return qgFlowControlMutationAddr("local") }
func (c *qgFlowControlMutationConn) RemoteAddr() net.Addr             { return qgFlowControlMutationAddr("remote") }
func (c *qgFlowControlMutationConn) SetDeadline(time.Time) error      { return nil }
func (c *qgFlowControlMutationConn) SetReadDeadline(time.Time) error  { return nil }
func (c *qgFlowControlMutationConn) SetWriteDeadline(time.Time) error { return nil }

func (c *qgFlowControlMutationConn) messages(t *testing.T) []protocol.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(c.writes)), "\n")
	messages := make([]protocol.Message, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var message protocol.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("decode flow-control worker message: %v", err)
		}
		messages = append(messages, message)
	}
	return messages
}

type qgFlowControlMutationAddr string

func (a qgFlowControlMutationAddr) Network() string { return "mutation" }
func (a qgFlowControlMutationAddr) String() string  { return string(a) }

type qgFlowControlMutationFixture struct {
	d            *Dispatcher
	db           *sql.DB
	store        *qgFlowControlMutationStore
	runner       *qgFlowControlMutationRunner
	conn         *qgFlowControlMutationConn
	assignmentID int64
	fixedNow     time.Time
}

func newQGFlowControlMutationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open flow-control database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		t.Fatalf("create flow-control schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE review_checkpoints (
		head_sha TEXT,
		target_sha TEXT,
		qg_evidence_path TEXT,
		qg_evidence_sha256 TEXT
	)`); err != nil {
		t.Fatalf("create flow-control review schema: %v", err)
	}
	return db
}

func newQGFlowControlMutationFixture(t *testing.T) *qgFlowControlMutationFixture {
	t.Helper()
	t.Setenv("ORO_BEADSOURCE_MODE", "cli")
	db := newQGFlowControlMutationDB(t)
	store := newQGFlowControlMutationStore()
	store.SetBeads([]protocol.Bead{{
		ID: qgFlowControlMutationBeadID, Title: "Flow control mutation", Type: "task",
		Status: "in_progress", Model: protocol.ModelOpus,
	}})
	d, err := New(
		Config{
			SocketPath: filepath.Join(t.TempDir(), "dispatcher.sock"),
			RepoRoot:   t.TempDir(), BeadsDir: protocol.BeadsDir, MaxWorkers: 1,
		},
		db, nil, nil, store, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("construct flow-control dispatcher: %v", err)
	}
	runner := &qgFlowControlMutationRunner{output: []byte(qgFlowControlMutationCandidate + "\n")}
	d.shutdownRunner = runner
	fixedNow := time.Date(2026, time.August, 9, 4, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return fixedNow }
	d.transientBackoffFn = func(int) time.Duration { return 0 }
	result, err := db.ExecContext(context.Background(),
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		qgFlowControlMutationBeadID, qgFlowControlMutationWorkerID, t.TempDir())
	if err != nil {
		t.Fatalf("seed flow-control assignment: %v", err)
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read flow-control assignment ID: %v", err)
	}
	conn := &qgFlowControlMutationConn{}
	d.workers[qgFlowControlMutationWorkerID] = &trackedWorker{
		id: qgFlowControlMutationWorkerID, conn: conn, state: protocol.WorkerBusy,
		assignmentID: assignmentID, beadID: qgFlowControlMutationBeadID,
		worktree: t.TempDir(), targetSHA: qgFlowControlMutationTargetSHA,
		targetBranch: "main", model: protocol.ModelOpus,
	}
	return &qgFlowControlMutationFixture{
		d: d, db: db, store: store, runner: runner, conn: conn,
		assignmentID: assignmentID, fixedNow: fixedNow,
	}
}

func qgFlowControlMutationEvaluationWithin(
	t *testing.T,
	d *Dispatcher,
	output string,
) qgFailureEvaluation {
	t.Helper()
	result := make(chan qgFailureEvaluation, 1)
	go func() {
		result <- d.evaluateQGFailure(
			context.Background(), qgFlowControlMutationWorkerID, qgFlowControlMutationBeadID, output,
		)
	}()
	select {
	case evaluation := <-result:
		return evaluation
	case <-time.After(2 * time.Second):
		t.Fatal("evaluateQGFailure did not return; possible local mutex deadlock")
		return qgFailureEvaluation{}
	}
}

func qgFlowControlMutationHandleWithin(t *testing.T, d *Dispatcher, output string) {
	t.Helper()
	done := make(chan struct{}, 1)
	go func() {
		d.handleQGFailure(
			context.Background(), qgFlowControlMutationWorkerID, qgFlowControlMutationBeadID, output,
		)
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleQGFailure did not return before local deadline")
	}
}

func qgFlowControlMutationMutexProbeWithin(t *testing.T, d *Dispatcher) {
	t.Helper()
	done := make(chan struct{}, 1)
	go func() {
		d.recordQGTargetPass("flow-control-unlock-probe")
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("flow-control evaluation retained the dispatcher mutex")
	}
}

func qgFlowControlMutationEventCount(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE type=?`, eventType).Scan(&count); err != nil {
		t.Fatalf("count flow-control event %q: %v", eventType, err)
	}
	return count
}

func qgFlowControlMutationAssignmentAttempt(t *testing.T, fixture *qgFlowControlMutationFixture) int {
	t.Helper()
	var attempt int
	if err := fixture.db.QueryRowContext(context.Background(),
		`SELECT attempt_count FROM assignments WHERE id=?`, fixture.assignmentID).Scan(&attempt); err != nil {
		t.Fatalf("read flow-control assignment attempt: %v", err)
	}
	return attempt
}

func qgFlowControlMutationOccurrenceCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM qg_failure_occurrences`).Scan(&count); err != nil {
		t.Fatalf("count flow-control occurrences: %v", err)
	}
	return count
}

func qgFlowControlMutationWorkerSnapshot(t *testing.T, d *Dispatcher) trackedWorker {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	worker := d.workers[qgFlowControlMutationWorkerID]
	if worker == nil {
		t.Fatal("flow-control worker disappeared")
	}
	return *worker
}

func TestQGFlowControlMutationOwner(t *testing.T) {
	t.Run("evaluation and target baseline", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.d.qgTargetObservations[qgFlowControlMutationTargetSHA] = qgTargetObservation{passed: true}
		got := qgFlowControlMutationEvaluationWithin(t, fixture.d, qgFlowControlMutationOutput)
		qgFlowControlMutationMutexProbeWithin(t, fixture.d)

		if got.err == nil || got.err.BeadID != qgFlowControlMutationBeadID ||
			got.err.WorkerID != qgFlowControlMutationWorkerID || got.err.Output != qgFlowControlMutationOutput {
			t.Fatalf("flow-control error = %+v, want exact worker, bead, and output", got.err)
		}
		if got.record.AssignmentID != fixture.assignmentID || got.record.Fingerprint == "" ||
			got.record.Output != qgFlowControlMutationOutput {
			t.Fatalf("flow-control record = %+v, want assignment %d and fingerprint", got.record, fixture.assignmentID)
		}
		if got.attribution.CandidateSHA != qgFlowControlMutationCandidate ||
			got.attribution.TargetSHA != qgFlowControlMutationTargetSHA ||
			!got.attribution.TargetKnown || !got.attribution.TargetPassed {
			t.Fatalf("flow-control attribution = %+v, want passing distinct target", got.attribution)
		}
		if got.classification.Class != QGFailureClassWorkerDeterministic ||
			got.classification.Decision != QGFailureDecisionRetryOriginal || got.targetBaselineFailure() {
			t.Fatalf("candidate-only classification = %+v, baseline=%t", got.classification, got.targetBaselineFailure())
		}

		base := qgFailureEvaluation{
			record: QGFailureRecord{Fingerprint: "qg:fingerprint"},
			attribution: QGFailureAttribution{
				CandidateSHA: "candidate", TargetSHA: "target", TargetKnown: true,
				TargetFingerprint: "qg:fingerprint",
			},
			classification: QGFailureClassification{
				Decision: QGFailureDecisionCreateOrReuseInfra, Confidence: QGFailureConfidenceHigh,
			},
		}
		if !base.targetBaselineFailure() {
			t.Fatal("matching high-confidence target failure was not recognized")
		}
		cases := []struct {
			name   string
			mutate func(*qgFailureEvaluation)
		}{
			{name: "decision mismatch", mutate: func(q *qgFailureEvaluation) {
				q.classification.Decision = QGFailureDecisionRetryOriginal
			}},
			{name: "confidence mismatch", mutate: func(q *qgFailureEvaluation) {
				q.classification.Confidence = QGFailureConfidenceMedium
			}},
			{name: "fingerprint mismatch", mutate: func(q *qgFailureEvaluation) {
				q.attribution.TargetFingerprint = "qg:other"
			}},
			{name: "target unknown", mutate: func(q *qgFailureEvaluation) {
				q.attribution.TargetKnown = false
			}},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				candidate := base
				tt.mutate(&candidate)
				if candidate.targetBaselineFailure() {
					t.Fatalf("targetBaselineFailure accepted %s", tt.name)
				}
			})
		}
	})

	t.Run("runner error is bounded", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.runner.mu.Lock()
		fixture.runner.err = errors.New("rev-parse failure")
		fixture.runner.mu.Unlock()
		got := qgFlowControlMutationEvaluationWithin(t, fixture.d, qgFlowControlMutationOutput)
		if got.attribution != (QGFailureAttribution{}) {
			t.Fatalf("runner-error attribution = %+v, want empty", got.attribution)
		}
	})

	t.Run("exact target failure short-circuits retry", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.runner.setOutput(qgFlowControlMutationTargetSHA + "\n")
		qgFlowControlMutationHandleWithin(t, fixture.d, qgFlowControlMutationOutput)

		worker := qgFlowControlMutationWorkerSnapshot(t, fixture.d)
		if !worker.lastProgress.Equal(fixture.fixedNow) {
			t.Fatalf("last progress = %v, want %v", worker.lastProgress, fixture.fixedNow)
		}
		if messages := fixture.conn.messages(t); len(messages) != 0 {
			t.Fatalf("exact-target failure sent retry messages: %+v", messages)
		}
		if got := qgFlowControlMutationAssignmentAttempt(t, fixture); got != 0 {
			t.Fatalf("exact-target failure persisted attempt %d, want 0", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "quality_gate_rejected"); got != 1 {
			t.Fatalf("quality-gate rejection events = %d, want 1", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_infra_incident_reused"); got != 1 {
			t.Fatalf("systemic exhaustion events = %d, want 1", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_retry_assign_sent"); got != 0 {
			t.Fatalf("exact-target failure emitted %d retry events, want 0", got)
		}
		if fixture.store.callsContaining("create:") == 0 {
			t.Fatal("exact-target failure did not create its durable infrastructure incident")
		}
	})

	t.Run("candidate-only failure persists and retries", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.d.qgTargetObservations[qgFlowControlMutationTargetSHA] = qgTargetObservation{passed: true}
		qgFlowControlMutationHandleWithin(t, fixture.d, qgFlowControlMutationOutput)

		messages := fixture.conn.messages(t)
		if len(messages) != 1 || messages[0].Type != protocol.MsgAssign ||
			messages[0].Assign == nil || messages[0].Assign.Attempt != 1 {
			t.Fatalf("candidate-only retry messages = %+v, want one attempt-1 ASSIGN", messages)
		}
		worker := qgFlowControlMutationWorkerSnapshot(t, fixture.d)
		if worker.state != protocol.WorkerBusy || worker.beadID != qgFlowControlMutationBeadID ||
			!worker.lastProgress.Equal(fixture.fixedNow) {
			t.Fatalf("candidate-only retry worker = %+v, want busy bead with refreshed progress", worker)
		}
		if got := qgFlowControlMutationAssignmentAttempt(t, fixture); got != 1 {
			t.Fatalf("candidate-only persisted attempt = %d, want 1", got)
		}
		if got := qgFlowControlMutationOccurrenceCount(t, fixture.db); got != 1 {
			t.Fatalf("candidate-only QG occurrences = %d, want 1", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_retry_assign_sent"); got != 1 {
			t.Fatalf("candidate-only retry events = %d, want 1", got)
		}
	})

	t.Run("stuck output stops normal retry", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.d.qgTargetObservations[qgFlowControlMutationTargetSHA] = qgTargetObservation{passed: true}
		hash := hashQGOutput(qgFlowControlMutationOutput)
		fixture.d.qgStuckTracker[qgFlowControlMutationBeadID] = &qgHistory{hashes: []string{hash, hash}}
		qgFlowControlMutationHandleWithin(t, fixture.d, qgFlowControlMutationOutput)

		if messages := fixture.conn.messages(t); len(messages) != 0 {
			t.Fatalf("stuck failure sent normal retry: %+v", messages)
		}
		if got := qgFlowControlMutationAssignmentAttempt(t, fixture); got != 0 {
			t.Fatalf("stuck failure persisted attempt %d, want 0", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_stuck_detected"); got != 1 {
			t.Fatalf("stuck-detected events = %d, want 1", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_retry_assign_sent"); got != 0 {
			t.Fatalf("stuck failure emitted %d normal retry events", got)
		}
	})

	t.Run("exhausted attempt stops normal retry", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.d.qgTargetObservations[qgFlowControlMutationTargetSHA] = qgTargetObservation{passed: true}
		fixture.d.attemptCounts[qgFlowControlMutationBeadID] = maxQGRetries - 1
		qgFlowControlMutationHandleWithin(t, fixture.d, qgFlowControlMutationOutput)

		if messages := fixture.conn.messages(t); len(messages) != 0 {
			t.Fatalf("exhausted failure sent retry: %+v", messages)
		}
		if got := qgFlowControlMutationAssignmentAttempt(t, fixture); got != maxQGRetries {
			t.Fatalf("exhausted persisted attempt = %d, want %d", got, maxQGRetries)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_retry_assign_sent"); got != 0 {
			t.Fatalf("exhausted failure emitted %d retry events", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_original_reopened"); got != 1 {
			t.Fatalf("exhausted reopen events = %d, want 1", got)
		}
		worker := qgFlowControlMutationWorkerSnapshot(t, fixture.d)
		if worker.state != protocol.WorkerIdle || worker.assignmentID != 0 || worker.beadID != "" {
			t.Fatalf("exhausted worker = %+v, want released idle worker", worker)
		}
		var assignmentStatus string
		if err := fixture.db.QueryRowContext(context.Background(),
			`SELECT status FROM assignments WHERE id=?`, fixture.assignmentID).Scan(&assignmentStatus); err != nil {
			t.Fatalf("read exhausted assignment status: %v", err)
		}
		if assignmentStatus != "completed" {
			t.Fatalf("exhausted assignment status = %q, want completed", assignmentStatus)
		}
	})

	t.Run("blocking dependency stops downstream effects", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		fixture.d.qgTargetObservations[qgFlowControlMutationTargetSHA] = qgTargetObservation{passed: true}
		fixture.store.SetBeads([]protocol.Bead{
			{
				ID: qgFlowControlMutationBeadID, Title: "Blocked flow control", Type: "task",
				Status: "in_progress", Model: protocol.ModelOpus,
				Dependencies: []protocol.Dependency{{
					IssueID: qgFlowControlMutationBeadID, DependsOnID: "flow-control-blocker", Type: "blocks",
				}},
			},
			{ID: "flow-control-blocker", Title: "Open blocker", Type: "task", Status: "open"},
		})
		qgFlowControlMutationHandleWithin(t, fixture.d, qgFlowControlMutationOutput)

		if messages := fixture.conn.messages(t); len(messages) != 0 {
			t.Fatalf("blocked failure sent retry: %+v", messages)
		}
		if got := qgFlowControlMutationAssignmentAttempt(t, fixture); got != 0 {
			t.Fatalf("blocked failure persisted downstream attempt %d, want 0", got)
		}
		if got := qgFlowControlMutationOccurrenceCount(t, fixture.db); got != 0 {
			t.Fatalf("blocked failure recorded %d downstream incidents, want 0", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_retry_blocked_by_dependency"); got != 1 {
			t.Fatalf("blocked dependency events = %d, want 1", got)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_retry_assign_sent"); got != 0 {
			t.Fatalf("blocked failure emitted %d retry events", got)
		}
	})

	t.Run("transient failure uses backoff retry budget", func(t *testing.T) {
		fixture := newQGFlowControlMutationFixture(t)
		qgFlowControlMutationHandleWithin(t, fixture.d, "network timeout while downloading module")

		messages := fixture.conn.messages(t)
		if len(messages) != 1 || messages[0].Type != protocol.MsgAssign {
			t.Fatalf("transient retry messages = %+v, want one ASSIGN", messages)
		}
		fixture.d.mu.Lock()
		transientCount := fixture.d.transientCounts[qgFlowControlMutationBeadID]
		attemptCount := fixture.d.attemptCounts[qgFlowControlMutationBeadID]
		fixture.d.mu.Unlock()
		if transientCount != 1 || attemptCount != 0 {
			t.Fatalf("transient/worker retry counts = %d/%d, want 1/0", transientCount, attemptCount)
		}
		if got := qgFlowControlMutationEventCount(t, fixture.db, "qg_transient_retry"); got != 1 {
			t.Fatalf("transient retry events = %d, want 1", got)
		}
	})
}
