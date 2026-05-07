package dispatcher_test

import (
	"context"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

func TestQGFailureStoreDedupesByFingerprint(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("SchemaDDL: %v", err)
	}

	cls := dispatcher.QGFailureClassification{
		Class:      dispatcher.QGFailureClassSystemic,
		Decision:   dispatcher.QGFailureDecisionCreateOrReuseInfra,
		Confidence: dispatcher.QGFailureConfidenceHigh,
		Reason:     "same fingerprint across beads",
	}
	rec := dispatcher.QGFailureRecord{
		ID:          "occ-1",
		Fingerprint: "qg:same",
		BeadID:      "oro-a",
		WorkerID:    "worker-a",
		Summary:     "package loader failure",
		Output:      "loader failed",
	}

	first, err := dispatcher.RecordQGFailureOccurrence(ctx, db, rec, cls)
	if err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if first.ID == 0 || first.OccurrenceCount != 1 {
		t.Fatalf("first incident = %+v, want id and occurrence_count=1", first)
	}

	rec.ID = "occ-2"
	rec.BeadID = "oro-b"
	second, err := dispatcher.RecordQGFailureOccurrence(ctx, db, rec, cls)
	if err != nil {
		t.Fatalf("Record second: %v", err)
	}
	if second.ID != first.ID || second.OccurrenceCount != 2 {
		t.Fatalf("second incident = %+v, want same id %d and occurrence_count=2", second, first.ID)
	}

	duplicate, err := dispatcher.RecordQGFailureOccurrence(ctx, db, rec, cls)
	if err != nil {
		t.Fatalf("Record duplicate: %v", err)
	}
	if duplicate.OccurrenceCount != 2 {
		t.Fatalf("duplicate occurrence count = %d, want 2", duplicate.OccurrenceCount)
	}

	if _, err := db.ExecContext(ctx, `UPDATE qg_failure_incidents SET status='closed' WHERE id=?`, first.ID); err != nil {
		t.Fatalf("close incident: %v", err)
	}
	rec.ID = "occ-3"
	rec.BeadID = "oro-c"
	reopened, err := dispatcher.RecordQGFailureOccurrence(ctx, db, rec, cls)
	if err != nil {
		t.Fatalf("Record closed recurrence: %v", err)
	}
	if reopened.ID != first.ID || reopened.Status != "open" || reopened.OccurrenceCount != 3 {
		t.Fatalf("reopened incident = %+v, want same id, open, occurrence_count=3", reopened)
	}

	var incidentRows, occurrenceRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents`).Scan(&incidentRows); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_occurrences`).Scan(&occurrenceRows); err != nil {
		t.Fatalf("count occurrences: %v", err)
	}
	if incidentRows != 1 || occurrenceRows != 3 {
		t.Fatalf("rows: incidents=%d occurrences=%d, want 1/3", incidentRows, occurrenceRows)
	}
}
