package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestThroughputHealthNormalizesMixedEventTimestamps(t *testing.T) {
	db, err := openStateDB(":memory:")
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer func() { _ = db.Close() }()

	insertThroughputAssignment(t, db, "oro-a", "worker-1", "2026-05-11 14:00:00")
	insertThroughputAssignment(t, db, "oro-b", "worker-2", "2026-05-11T14:05:00Z")
	insertThroughputAssignment(t, db, "oro-c", "worker-3", "2026-05-11T14:10:00Z")
	insertThroughputBead(t, db, "oro-a", "closed", "Merged ff-only", "2026-05-11 14:20:00")
	insertThroughputBead(t, db, "oro-b", "closed", "DEFERRED/Duplicate close", "2026-05-11T14:25:00Z")
	insertThroughputBead(t, db, "oro-old", "closed", "Merged", "2026-05-11T11:00:00Z")
	insertThroughputEvent(t, db, "quality_gate_rejected", "oro-a", "worker-1", `{"fingerprint":"fp_qg_1"}`, "2026-05-11 14:15:00")
	insertThroughputEvent(t, db, "quality_gate_rejected", "oro-a", "worker-1", `{"fingerprint":"fp_qg_1"}`, "2026-05-11T14:16:00Z")
	insertThroughputEvent(t, db, "review_rejected", "oro-b", "worker-2", "needs fix", "2026-05-11T14:17:00Z")
	insertThroughputEvent(t, db, "progress_timeout", "oro-c", "worker-3", "stuck", "2026-05-11 14:18:00")
	insertThroughputEvent(t, db, "quality_gate_rejected", "oro-b", "worker-2", `{"fingerprint":"fp_qg_2"}`, "not-a-time")

	health, err := computeThroughputHealth(context.Background(), db, time.Hour)
	if err != nil {
		t.Fatalf("computeThroughputHealth: %v", err)
	}

	if health.Assignments != 3 {
		t.Fatalf("assignments = %d, want 3", health.Assignments)
	}
	if health.ProductiveClosures != 1 || health.DeferredClosures != 1 {
		t.Fatalf("closures productive/deferred = %d/%d, want 1/1", health.ProductiveClosures, health.DeferredClosures)
	}
	if health.QGRejections != 2 || health.ReviewRejections != 1 || health.ProgressTimeouts != 1 {
		t.Fatalf("reject/timeout counts qg/review/progress = %d/%d/%d, want 2/1/1", health.QGRejections, health.ReviewRejections, health.ProgressTimeouts)
	}
	if health.TimestampWarningCount != 1 {
		t.Fatalf("timestamp warnings = %d, want 1", health.TimestampWarningCount)
	}
	if health.ProductivePerAssignment != 1.0/3.0 {
		t.Fatalf("productive_per_assignment = %v, want 1/3", health.ProductivePerAssignment)
	}
	if health.QGRejectionsPerAssignment != 2.0/3.0 || health.ReviewRejectionsPerAssignment != 1.0/3.0 || health.ProgressTimeoutsPerAssignment != 1.0/3.0 {
		t.Fatalf("per-assignment ratios = qg:%v review:%v progress:%v", health.QGRejectionsPerAssignment, health.ReviewRejectionsPerAssignment, health.ProgressTimeoutsPerAssignment)
	}
	if len(health.TopRepeatedBeads) == 0 || health.TopRepeatedBeads[0].Key != "oro-a" || health.TopRepeatedBeads[0].Count != 2 {
		t.Fatalf("top repeated beads = %+v, want oro-a count 2", health.TopRepeatedBeads)
	}
	if len(health.TopRepeatedFingerprints) == 0 || health.TopRepeatedFingerprints[0].Key != "fp_qg_1" || health.TopRepeatedFingerprints[0].Count != 2 {
		t.Fatalf("top repeated fingerprints = %+v, want fp_qg_1 count 2", health.TopRepeatedFingerprints)
	}
	if health.Baseline.Name != "May-11" || health.Baseline.ProductivePerAssignmentDelta >= 0 {
		t.Fatalf("baseline = %+v, want May-11 negative productive delta", health.Baseline)
	}
}

func TestThroughputHealthAssertMode(t *testing.T) {
	pass := ThroughputHealth{
		Assignments:                   4,
		ProductivePerAssignment:       0.50,
		QGRejectionsPerAssignment:     0.25,
		ReviewRejectionsPerAssignment: 0.25,
		ProgressTimeoutsPerAssignment: 0,
	}
	fail := pass
	fail.ProductivePerAssignment = 0.10
	fail.QGRejectionsPerAssignment = 1.25

	cfg := throughputAssertConfig{
		MinProductivePerAssignment:       0.25,
		MaxQGRejectionsPerAssignment:     0.50,
		MaxReviewRejectionsPerAssignment: 0.50,
		MaxProgressTimeoutsPerAssignment: 0.10,
	}
	if err := assertThroughputHealth(pass, cfg); err != nil {
		t.Fatalf("passing throughput health failed assert: %v", err)
	}
	err := assertThroughputHealth(fail, cfg)
	if err == nil {
		t.Fatal("expected assert failure")
	}
	if !strings.Contains(err.Error(), "productive_per_assignment") || !strings.Contains(err.Error(), "qg_rejections_per_assignment") {
		t.Fatalf("assert error = %v, want threshold names", err)
	}

	zeroMax := throughputAssertConfig{
		MaxProgressTimeoutsPerAssignment: 0,
		MaxQGRejectionsPerAssignment:     -1,
		MaxReviewRejectionsPerAssignment: -1,
	}
	zeroFail := ThroughputHealth{Assignments: 1, ProgressTimeoutsPerAssignment: 1}
	err = assertThroughputHealth(zeroFail, zeroMax)
	if err == nil {
		t.Fatal("expected explicit zero max progress timeout threshold to fail")
	}
	if !strings.Contains(err.Error(), "progress_timeouts_per_assignment") {
		t.Fatalf("zero max assert error = %v, want progress timeout threshold name", err)
	}
}

func TestThroughputCommandMaxThresholdDefaultsDisabled(t *testing.T) {
	cmd := newThroughputCmd()
	for _, name := range []string{
		"max-qg-rejections-per-assignment",
		"max-review-rejections-per-assignment",
		"max-progress-timeouts-per-assignment",
	} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("missing flag %s", name)
		}
		if flag.DefValue != "-1" {
			t.Fatalf("%s default = %q, want -1 so omitted max thresholds are disabled", name, flag.DefValue)
		}
	}
}

func insertThroughputAssignment(t *testing.T, db *sql.DB, beadID, workerID, assignedAt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, assigned_at) VALUES (?, ?, ?, 'completed', ?)`,
		beadID, workerID, "/tmp/"+beadID, assignedAt); err != nil {
		t.Fatalf("insert assignment %s: %v", beadID, err)
	}
}

func insertThroughputBead(t *testing.T, db *sql.DB, beadID, status, closeReason, closedAt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO beads (id, title, status, priority, type, close_reason, created_at, updated_at, closed_at)
		 VALUES (?, ?, ?, 0, 'task', ?, '2026-05-11T13:00:00Z', ?, ?)`,
		beadID, beadID, status, closeReason, closedAt, closedAt); err != nil {
		t.Fatalf("insert bead %s: %v", beadID, err)
	}
}

func insertThroughputEvent(t *testing.T, db *sql.DB, eventType, beadID, workerID, payload, createdAt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO events (type, source, bead_id, worker_id, payload, created_at) VALUES (?, 'test', ?, ?, ?, ?)`,
		eventType, beadID, workerID, payload, createdAt); err != nil {
		t.Fatalf("insert event %s/%s: %v", eventType, beadID, err)
	}
}
