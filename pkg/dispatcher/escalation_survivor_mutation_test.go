package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

type escalationMutationEscalator struct {
	messages []string
	err      error
}

func (e *escalationMutationEscalator) Escalate(_ context.Context, msg string) error {
	e.messages = append(e.messages, msg)
	return e.err
}

func newEscalationMutationHarness(t *testing.T) (*Dispatcher, *sql.DB, *escalationMutationEscalator) {
	t.Helper()
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open dispatcher database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("initialize dispatcher schema: %v", err)
	}
	if err := protocol.InitializeBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("initialize bead schema: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemorySearchEvents); err != nil {
		t.Fatalf("initialize semantic search events: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("initialize semantic read events: %v", err)
	}
	escalator := &escalationMutationEscalator{}
	d, err := New(Config{
		RepoRoot:          t.TempDir(),
		ReviewEvidenceDir: filepath.Join(t.TempDir(), "review-evidence"),
		MaxWorkers:        1,
		DefaultBranch:     "main",
	}, db, nil, nil, beadstore.NewSQLiteStore(db), nil, escalator, nil)
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	return d, db, escalator
}

func TestEscalationSurvivorMutationPureContracts(t *testing.T) {
	for _, tc := range []struct {
		name, message, oneShot, stored string
	}{
		{name: "missing prefix", message: "MISSING_AC: b-1", oneShot: "", stored: ""},
		{name: "missing separator", message: "[ORO-DISPATCH] MISSING_AC", oneShot: "", stored: ""},
		{name: "recognized", message: "[ORO-DISPATCH] MISSING_AC: b-1", oneShot: string(protocol.EscMissingAC), stored: string(protocol.EscMissingAC)},
		{name: "stored without playbook", message: "[ORO-DISPATCH] WORKER_CRASH: b-1", oneShot: "", stored: string(protocol.EscWorkerCrash)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseEscalationType(tc.message); got != tc.oneShot {
				t.Fatalf("parseEscalationType(%q) = %q, want %q", tc.message, got, tc.oneShot)
			}
			if got := extractEscalationType(tc.message); got != tc.stored {
				t.Fatalf("extractEscalationType(%q) = %q, want %q", tc.message, got, tc.stored)
			}
		})
	}

	for _, tc := range []struct {
		acceptance string
		want       bool
	}{
		{acceptance: "Test: unit\nCmd: go test\nAssert: pass", want: true},
		{acceptance: "Cmd: go test\nAssert: pass", want: false},
		{acceptance: "Test: unit\nAssert: pass", want: false},
		{acceptance: "Test: unit\nCmd: go test", want: false},
	} {
		if got := hasTDDAcceptance(tc.acceptance); got != tc.want {
			t.Fatalf("hasTDDAcceptance(%q) = %t, want %t", tc.acceptance, got, tc.want)
		}
	}

	if got, ok := routedOpsRunType(protocol.EscOversizedBead); !ok || got != ops.OpsDecompose {
		t.Fatalf("routed oversized type = %q, %t", got, ok)
	}
	if got, ok := routedOpsRunType(protocol.EscMissingAC); ok || got != "" {
		t.Fatalf("routed missing-AC type = %q, %t", got, ok)
	}
	if !isRoutableEscalationType(protocol.EscOversizedBead) || isRoutableEscalationType(protocol.EscMissingAC) {
		t.Fatal("routable escalation classification mismatch")
	}
	if !isInformationalEscalation(protocol.EscMergeComplete) || !isInformationalEscalation(protocol.EscManualIntegration) || isInformationalEscalation(protocol.EscMergeConflict) {
		t.Fatal("informational escalation classification mismatch")
	}

	if got, ok := opsRunTypeForEscalationResult("future", ops.Result{Type: ops.OpsAudit}); !ok || got != ops.OpsAudit {
		t.Fatalf("explicit result type = %q, %t", got, ok)
	}
	for _, tc := range []struct {
		escType string
		want    ops.Type
	}{
		{escType: string(protocol.EscMissingAC), want: ops.OpsWriteAC},
		{escType: string(protocol.EscOversizedBead), want: ops.OpsDecompose},
		{escType: string(protocol.EscStuckWorker), want: ops.OpsEscalation},
	} {
		if got, ok := opsRunTypeForEscalationResult(tc.escType, ops.Result{}); !ok || got != tc.want {
			t.Fatalf("inferred result type for %q = %q, %t", tc.escType, got, ok)
		}
	}
	if got, ok := opsRunTypeForEscalationResult("future", ops.Result{}); ok || got != "" {
		t.Fatalf("unknown result type = %q, %t", got, ok)
	}

	d := &Dispatcher{}
	if !d.shouldSkipPendingEscalationRetry("future") || d.shouldSkipPendingEscalationRetry(string(protocol.EscMergeConflict)) || d.shouldSkipPendingEscalationRetry(string(protocol.EscOversizedBead)) {
		t.Fatal("pending retry classification without ops mismatch")
	}
	d.ops = ops.NewSpawner(nil)
	if !d.shouldSkipPendingEscalationRetry(string(protocol.EscOversizedBead)) || d.shouldSkipPendingEscalationRetry(string(protocol.EscMergeConflict)) {
		t.Fatal("pending retry classification with ops mismatch")
	}
}

func TestEscalationSurvivorMutationPersistenceContracts(t *testing.T) {
	d, db, _ := newEscalationMutationHarness(t)
	ctx := t.Context()
	id := d.insertEscalation(ctx, string(protocol.EscMergeConflict), "bead-persist", "worker-persist", "persist-message")
	if id <= 0 {
		t.Fatalf("insert escalation id = %d", id)
	}
	encoded, err := d.applyPendingEscalations()
	if err != nil {
		t.Fatalf("apply pending escalations: %v", err)
	}
	var pending []protocol.Escalation
	if err := json.Unmarshal([]byte(encoded), &pending); err != nil {
		t.Fatalf("decode pending escalations: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id || pending[0].Type != string(protocol.EscMergeConflict) || pending[0].BeadID != "bead-persist" || pending[0].WorkerID != "worker-persist" || pending[0].Message != "persist-message" {
		t.Fatalf("pending escalations = %+v", pending)
	}

	if _, err := d.applyAckEscalation(" \t "); err == nil || !strings.Contains(err.Error(), "requires an escalation ID") {
		t.Fatalf("blank ack error = %v", err)
	}
	if got, err := d.applyAckEscalation("999999"); err != nil || got != "escalation 999999 not found or already acked" {
		t.Fatalf("unknown ack = %q, %v", got, err)
	}
	idText := strconv.FormatInt(id, 10)
	if got, err := d.applyAckEscalation("  " + idText + "  "); err != nil || got != "acked escalation "+idText {
		t.Fatalf("valid ack = %q, %v", got, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id=?`, id).Scan(&status); err != nil || status != "acked" {
		t.Fatalf("acked row status = %q, %v", status, err)
	}

	second := d.insertEscalation(ctx, string(protocol.EscWorkerCrash), "bead-ack", "worker-ack", "ack-message")
	d.ackEscalation(ctx, second, "bead-ack", "worker-ack")
	var eventPayload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE type='escalation_acked' AND bead_id='bead-ack'`).Scan(&eventPayload); err != nil || !strings.Contains(eventPayload, `"rows_affected":1`) {
		t.Fatalf("ack event payload = %q, %v", eventPayload, err)
	}
	d.ackEscalation(ctx, 0, "bead-zero", "worker-zero")
	var zeroEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE bead_id='bead-zero'`).Scan(&zeroEvents); err != nil || zeroEvents != 0 {
		t.Fatalf("zero-id ack events = %d, %v", zeroEvents, err)
	}
}

func TestEscalationSurvivorMutationRetryContracts(t *testing.T) {
	d, db, escalator := newEscalationMutationHarness(t)
	ctx := t.Context()
	rows := []struct {
		escType, beadID, message string
	}{
		{escType: string(protocol.EscMergeComplete), beadID: "done", message: "resolved"},
		{escType: string(protocol.EscMergeConflict), beadID: "", message: "retry-me"},
		{escType: string(protocol.EscOversizedBead), beadID: "stale", message: "drain-me"},
		{escType: "FUTURE_TYPE", beadID: "future", message: "skip-me"},
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		res, err := db.ExecContext(ctx, `INSERT INTO escalations(type, bead_id, worker_id, message) VALUES(?,?,?,?)`, row.escType, row.beadID, "worker", row.message)
		if err != nil {
			t.Fatalf("seed escalation: %v", err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	d.retryPendingEscalations(ctx)
	if len(escalator.messages) != 1 || escalator.messages[0] != "retry-me" {
		t.Fatalf("retried messages = %v", escalator.messages)
	}
	for i, want := range []struct {
		status string
		retry  int
	}{{"acked", 0}, {"pending", 1}, {"acked", 0}, {"pending", 0}} {
		var status string
		var retry int
		if err := db.QueryRow(`SELECT status, retry_count FROM escalations WHERE id=?`, ids[i]).Scan(&status, &retry); err != nil || status != want.status || retry != want.retry {
			t.Fatalf("row %d = status %q retry %d, want %q/%d: %v", i, status, retry, want.status, want.retry, err)
		}
	}
}

func TestEscalationSurvivorMutationDecomposeValidationContracts(t *testing.T) {
	ctx := t.Context()
	if err := (*Dispatcher)(nil).validateDecomposeResult(ctx, "nil-dispatcher"); !errors.Is(err, errDecomposeValidationUnavailable) {
		t.Fatalf("nil dispatcher validation error = %v", err)
	}

	t.Run("parent and child invariants", func(t *testing.T) {
		d, db, _ := newEscalationMutationHarness(t)
		if err := d.validateDecomposeResult(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "parent bead missing") {
			t.Fatalf("missing parent error = %v", err)
		}
		insertMutationBead(t, db, "plain", "task", "open", "", "")
		if err := d.validateDecomposeResult(ctx, "plain"); err == nil || !strings.Contains(err.Error(), "non-epic parent has no child tasks") {
			t.Fatalf("plain parent error = %v", err)
		}
		insertMutationBead(t, db, "empty-epic", "epic", "open", "", "")
		if err := d.validateDecomposeResult(ctx, "empty-epic"); err != nil {
			t.Fatalf("empty epic validation: %v", err)
		}
		insertMutationBead(t, db, "closed-parent", "task", "closed", "", "")
		if err := d.validateDecomposeResult(ctx, "closed-parent"); err != nil {
			t.Fatalf("closed parent validation: %v", err)
		}

		insertMutationBead(t, db, "parent", "task", "open", "", "")
		insertMutationBead(t, db, "child-b", "task", "open", "parent", "Test: unit\nCmd: go test\nAssert: pass")
		insertMutationBead(t, db, "child-a", "task", "open", "parent", "Test: unit\nCmd: go test\nAssert: pass")
		children, err := d.loadDecomposeChildren(ctx, "parent")
		if err != nil || len(children) != 2 || children[0].ID != "child-a" || children[1].ID != "child-b" {
			t.Fatalf("loaded children = %+v, %v", children, err)
		}
		if err := d.validateDecomposeResult(ctx, "parent"); err == nil || !strings.Contains(err.Error(), "does not depend on child child-a") {
			t.Fatalf("missing dependency error = %v", err)
		}
		if _, err := db.Exec(`INSERT INTO bead_deps(bead_id, depends_on_id, type) VALUES('parent','child-a','blocks'),('parent','child-b','conditional-blocks')`); err != nil {
			t.Fatalf("seed dependencies: %v", err)
		}
		if err := d.validateDecomposeResult(ctx, "parent"); err != nil {
			t.Fatalf("valid decomposition: %v", err)
		}
		if ok, err := d.parentDependsOnChild(ctx, "parent", "child-a"); err != nil || !ok {
			t.Fatalf("blocks dependency = %t, %v", ok, err)
		}
		if ok, err := d.parentDependsOnChild(ctx, "parent", "missing"); err != nil || ok {
			t.Fatalf("missing dependency = %t, %v", ok, err)
		}
	})

	for _, missing := range []string{"Test:", "Cmd:", "Assert:"} {
		t.Run("missing "+missing, func(t *testing.T) {
			d, db, _ := newEscalationMutationHarness(t)
			insertMutationBead(t, db, "parent", "task", "open", "", "")
			acceptance := "Test: unit\nCmd: go test\nAssert: pass"
			acceptance = strings.ReplaceAll(acceptance, missing, "Absent:")
			insertMutationBead(t, db, "child", "task", "open", "parent", acceptance)
			if err := d.validateDecomposeResult(ctx, "parent"); err == nil || !strings.Contains(err.Error(), "acceptance criteria") {
				t.Fatalf("missing %s validation error = %v", missing, err)
			}
		})
	}
}

func insertMutationBead(t *testing.T, db *sql.DB, id, beadType, status, parentID, acceptance string) {
	t.Helper()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	if _, err := db.Exec(`INSERT INTO beads(id, title, type, status, parent_id, acceptance_criteria) VALUES(?,?,?,?,?,?)`, id, id, beadType, status, parent, acceptance); err != nil {
		t.Fatalf("insert bead %s: %v", id, err)
	}
}

func TestEscalationSurvivorMutationOpsRunCompletionContracts(t *testing.T) {
	d, db, _ := newEscalationMutationHarness(t)
	ctx := t.Context()
	if !d.completeOpsRunBestEffort(ctx, 0, 0, ops.OpsDecompose, "zero", "worker", opsRunStatusResolved, "approved", "feedback", "") {
		t.Fatal("zero ops-run ID should be a successful no-op")
	}
	escalationID := d.insertEscalation(ctx, string(protocol.EscOversizedBead), "ops-bead", "ops-worker", "split")
	rec, created, err := CreateOpsRun(ctx, db, OpsRunRecord{
		Type: string(ops.OpsDecompose), BeadID: "ops-bead", WorkerID: "ops-worker", Status: opsRunStatusRunning,
	})
	if err != nil || !created {
		t.Fatalf("create ops run = %+v, %t, %v", rec, created, err)
	}
	if !d.completeOpsRunBestEffort(ctx, rec.ID, escalationID, ops.OpsDecompose, "ops-bead", "ops-worker", opsRunStatusResolved, "approved", "feedback", "") {
		t.Fatal("complete running ops run returned false")
	}
	var status, verdict, feedback string
	var linked sql.NullInt64
	if err := db.QueryRow(`SELECT status, verdict, feedback, escalation_id FROM ops_runs WHERE id=?`, rec.ID).Scan(&status, &verdict, &feedback, &linked); err != nil || status != opsRunStatusResolved || verdict != "approved" || feedback != "feedback" || !linked.Valid || linked.Int64 != escalationID {
		t.Fatalf("completed ops run = %q/%q/%q/%v, %v", status, verdict, feedback, linked, err)
	}
	if d.completeOpsRunBestEffort(ctx, rec.ID, escalationID, ops.OpsDecompose, "ops-bead", "ops-worker", opsRunStatusFailed, "failed", "late", "late") {
		t.Fatal("terminal ops run accepted a different completion")
	}

	failureResult := ops.Result{Type: ops.OpsEscalation, Feedback: "spawn failed", Err: errors.New("boom")}
	if !d.completeOneShotOpsRunFailureBestEffort(ctx, 0, 0, string(protocol.EscStuckWorker), "failure-bead", "failure-worker", failureResult) {
		t.Fatal("persist fresh failed ops run returned false")
	}
	var failureStatus, failureVerdict, failureError string
	if err := db.QueryRow(`SELECT status, verdict, error FROM ops_runs WHERE bead_id='failure-bead'`).Scan(&failureStatus, &failureVerdict, &failureError); err != nil || failureStatus != opsRunStatusFailed || failureVerdict != string(ops.VerdictFailed) || failureError != "boom" {
		t.Fatalf("failed ops run = %q/%q/%q, %v", failureStatus, failureVerdict, failureError, err)
	}
}

func TestEscalationSurvivorMutationFailureAndCleanupContracts(t *testing.T) {
	d, db, _ := newEscalationMutationHarness(t)
	ctx := t.Context()
	escalationID := d.insertEscalation(ctx, string(protocol.EscMissingAC), "failure-bead", "failure-worker", "missing AC")
	d.handleFailedEscalationResult(ctx, 0, escalationID, string(protocol.EscMissingAC), "failure-bead", "failure-worker", ops.Result{Type: ops.OpsWriteAC, Err: errors.New("write failed")})
	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id=?`, escalationID).Scan(&status); err != nil || status != "acked" {
		t.Fatalf("failed escalation status = %q, %v", status, err)
	}
	if _, ok := d.worktreeFailures["failure-bead"]; !ok {
		t.Fatal("failed missing-AC result did not record assignment failure")
	}
	var failedEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='oneshot_escalation_failed' AND bead_id='failure-bead'`).Scan(&failedEvents); err != nil || failedEvents != 1 {
		t.Fatalf("failed escalation events = %d, %v", failedEvents, err)
	}
	d.clearAssignmentFailure("failure-bead")
	if _, ok := d.worktreeFailures["failure-bead"]; ok {
		t.Fatal("clearAssignmentFailure retained the bead")
	}
	lockAvailable := make(chan struct{})
	go func() {
		d.mu.Lock()
		d.mu.Unlock()
		close(lockAvailable)
	}()
	select {
	case <-lockAvailable:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("clearAssignmentFailure retained the dispatcher mutex")
	}
}

func TestEscalationSurvivorMutationRoutableDedupContracts(t *testing.T) {
	d, db, _ := newEscalationMutationHarness(t)
	ctx := t.Context()
	if d.routeNewRoutableEscalation(ctx, protocol.EscOversizedBead, "nil-ops", "worker", "split") {
		t.Fatal("nil ops spawner routed an escalation")
	}
	d.ops = ops.NewSpawner(nil)
	if d.routeNewRoutableEscalation(ctx, protocol.EscMissingAC, "not-routable", "worker", "write AC") {
		t.Fatal("non-routable escalation was routed")
	}
	if d.routeNewRoutableEscalation(ctx, protocol.EscOversizedBead, "", "worker", "split") {
		t.Fatal("empty bead escalation was routed")
	}

	if _, _, err := d.createRoutableOpsRun(ctx, 0, protocol.EscMissingAC, "invalid", "worker", "write AC"); err == nil || !strings.Contains(err.Error(), "unsupported routable escalation") {
		t.Fatalf("unsupported routed ops-run error = %v", err)
	}
	rec, created, err := d.createRoutableOpsRun(ctx, 0, protocol.EscOversizedBead, "dedup-bead", "dedup-worker", "split")
	if err != nil || !created || rec.ID <= 0 || rec.Type != string(ops.OpsDecompose) {
		t.Fatalf("created routed ops run = %+v, %t, %v", rec, created, err)
	}
	escalationID := d.insertEscalation(ctx, string(protocol.EscOversizedBead), "dedup-bead", "dedup-worker", "split")
	if err := d.routeExistingRoutableEscalation(ctx, escalationID, protocol.EscOversizedBead, "dedup-bead", "dedup-worker", "split"); err != nil {
		t.Fatalf("route existing blocked escalation: %v", err)
	}
	var escalationStatus string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id=?`, escalationID).Scan(&escalationStatus); err != nil || escalationStatus != "acked" {
		t.Fatalf("blocked escalation status = %q, %v", escalationStatus, err)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE type='ops_run_blocked_assignment' AND bead_id='dedup-bead'`).Scan(&payload); err != nil || !strings.Contains(payload, `"ops_run_id":`+strconv.FormatInt(rec.ID, 10)) || !strings.Contains(payload, `"escalation_id":`+strconv.FormatInt(escalationID, 10)) {
		t.Fatalf("blocked assignment event = %q, %v", payload, err)
	}

	d.populateOpsRunEscalationIDBestEffort(ctx, rec.ID, 0, ops.OpsDecompose, "dedup-bead", "dedup-worker")
	var updateFailures int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='ops_run_update_failed' AND bead_id='dedup-bead'`).Scan(&updateFailures); err != nil || updateFailures != 0 {
		t.Fatalf("zero escalation update failures = %d, %v", updateFailures, err)
	}
}

func TestEscalationSurvivorMutationDecomposeResultTransitions(t *testing.T) {
	t.Run("failed verdict", func(t *testing.T) {
		d, db, _ := newEscalationMutationHarness(t)
		ctx := t.Context()
		id := d.insertEscalation(ctx, string(protocol.EscOversizedBead), "failed-decompose", "worker", "split")
		d.handleDecomposeResult(ctx, 0, id, string(protocol.EscOversizedBead), "failed-decompose", "worker", ops.Result{
			Type: ops.OpsDecompose, Verdict: ops.VerdictFailed, Feedback: "cannot split",
		})
		assertEscalationMutationStatus(t, db, id, "acked")
		if _, ok := d.worktreeFailures["failed-decompose"]; !ok {
			t.Fatal("failed decompose did not record assignment failure")
		}
		assertEscalationMutationEvent(t, db, "oneshot_escalation_complete", "failed-decompose")
	})

	t.Run("valid result", func(t *testing.T) {
		d, db, _ := newEscalationMutationHarness(t)
		ctx := t.Context()
		insertMutationBead(t, db, "valid-decompose", "epic", "open", "", "")
		d.worktreeFailures["valid-decompose"] = d.nowFunc()
		id := d.insertEscalation(ctx, string(protocol.EscOversizedBead), "valid-decompose", "worker", "split")
		d.handleDecomposeResult(ctx, 0, id, string(protocol.EscOversizedBead), "valid-decompose", "worker", ops.Result{
			Type: ops.OpsDecompose, Verdict: ops.VerdictApproved, Feedback: "split complete",
		})
		assertEscalationMutationStatus(t, db, id, "acked")
		if _, ok := d.worktreeFailures["valid-decompose"]; ok {
			t.Fatal("valid decompose retained assignment failure")
		}
		assertEscalationMutationEvent(t, db, "oneshot_escalation_complete", "valid-decompose")
	})

	t.Run("validation unavailable", func(t *testing.T) {
		d, db, _ := newEscalationMutationHarness(t)
		ctx := t.Context()
		d.worktreeFailures["unavailable-decompose"] = d.nowFunc()
		id := d.insertEscalation(ctx, string(protocol.EscOversizedBead), "unavailable-decompose", "worker", "split")
		d.handleDecomposeValidationError(ctx, 0, id, string(protocol.EscOversizedBead), "unavailable-decompose", "worker", ops.Result{
			Type: ops.OpsDecompose, Verdict: ops.VerdictApproved, Feedback: "split",
		}, fmt.Errorf("%w: schema absent", errDecomposeValidationUnavailable))
		assertEscalationMutationStatus(t, db, id, "acked")
		if _, ok := d.worktreeFailures["unavailable-decompose"]; ok {
			t.Fatal("unavailable validation retained assignment failure")
		}
		assertEscalationMutationEvent(t, db, "oneshot_escalation_validation_skipped", "unavailable-decompose")
	})

	t.Run("validation failed", func(t *testing.T) {
		d, db, _ := newEscalationMutationHarness(t)
		ctx := t.Context()
		id := d.insertEscalation(ctx, string(protocol.EscOversizedBead), "invalid-decompose", "worker", "split")
		d.handleDecomposeValidationError(ctx, 0, id, string(protocol.EscOversizedBead), "invalid-decompose", "worker", ops.Result{
			Type: ops.OpsDecompose, Verdict: ops.VerdictApproved, Feedback: "bad split",
		}, errors.New("children invalid"))
		assertEscalationMutationStatus(t, db, id, "pending")
		if _, ok := d.worktreeFailures["invalid-decompose"]; !ok {
			t.Fatal("invalid validation did not record assignment failure")
		}
		assertEscalationMutationEvent(t, db, "oneshot_escalation_validation_failed", "invalid-decompose")
		assertEscalationMutationEvent(t, db, "oneshot_escalation_complete", "invalid-decompose")
	})
}

func assertEscalationMutationStatus(t *testing.T, db *sql.DB, id int64, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id=?`, id).Scan(&got); err != nil || got != want {
		t.Fatalf("escalation %d status = %q, want %q: %v", id, got, want, err)
	}
}

func assertEscalationMutationEvent(t *testing.T, db *sql.DB, eventType, beadID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%s events for %s = %d, want 1: %v", eventType, beadID, count, err)
	}
}
