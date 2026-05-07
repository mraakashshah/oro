package dispatcher

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// QGIncident is the persisted incident row for a deduped QG failure.
type QGIncident struct {
	ID              int64
	Fingerprint     string
	Class           QGFailureClass
	Decision        QGFailureDecision
	Confidence      QGFailureConfidence
	Reason          string
	Summary         string
	Status          string
	OccurrenceCount int
}

// RecordQGFailureOccurrence stores a QG failure occurrence and dedupes the
// owning incident by fingerprint.
func RecordQGFailureOccurrence(ctx context.Context, db *sql.DB, rec QGFailureRecord, cls QGFailureClassification) (QGIncident, error) {
	if db == nil {
		return QGIncident{}, errors.New("record qg failure occurrence: nil db")
	}
	rec = normalizeQGFailureRecord(rec)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	incident, err := recordQGFailureOccurrenceTx(ctx, tx, rec, cls)
	if err != nil {
		return QGIncident{}, err
	}
	if err := tx.Commit(); err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: commit: %w", err)
	}
	return incident, nil
}

func normalizeQGFailureRecord(rec QGFailureRecord) QGFailureRecord {
	if rec.Fingerprint == "" || rec.Summary == "" {
		fp, summary := FingerprintQGFailure(rec.Output, QGFingerprintOptions{})
		if rec.Fingerprint == "" {
			rec.Fingerprint = fp
		}
		if rec.Summary == "" {
			rec.Summary = summary
		}
	}
	if rec.OutputHash == "" {
		rec.OutputHash = hashQGFailureOutput(rec.Output)
	}
	if rec.ID == "" {
		rec.ID = qgOccurrenceID(rec)
	}
	return rec
}

func recordQGFailureOccurrenceTx(ctx context.Context, tx *sql.Tx, rec QGFailureRecord, cls QGFailureClassification) (QGIncident, error) {
	incidentID, err := ensureQGIncident(ctx, tx, rec, cls)
	if err != nil {
		return QGIncident{}, err
	}
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO qg_failure_occurrences
    (id, incident_id, bead_id, worker_id, assignment_id, component, output_hash, raw_output)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, incidentID, nullableString(rec.BeadID), nullableString(rec.WorkerID), nullableInt64(rec.AssignmentID), nullableString(rec.Component), rec.OutputHash, rec.Output)
	if err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: insert occurrence: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: rows affected: %w", err)
	}
	if inserted > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE qg_failure_incidents
   SET occurrence_count = occurrence_count + 1,
       last_seen = datetime('now'),
       status = 'open',
       class = ?,
       decision = ?,
       confidence = ?,
       reason = ?,
       summary = ?
 WHERE id = ?`,
			string(cls.Class), string(cls.Decision), string(cls.Confidence), cls.Reason, rec.Summary, incidentID); err != nil {
			return QGIncident{}, fmt.Errorf("record qg failure occurrence: update incident: %w", err)
		}
	}

	incident, err := fetchQGIncident(ctx, tx, incidentID)
	if err != nil {
		return QGIncident{}, err
	}
	return incident, nil
}

func ensureQGIncident(ctx context.Context, tx *sql.Tx, rec QGFailureRecord, cls QGFailureClassification) (int64, error) {
	var incidentID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM qg_failure_incidents WHERE fingerprint=?`, rec.Fingerprint).Scan(&incidentID)
	if err == nil {
		return incidentID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("record qg failure occurrence: query incident: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO qg_failure_incidents
    (fingerprint, class, decision, confidence, reason, summary)
VALUES (?, ?, ?, ?, ?, ?)`,
		rec.Fingerprint, string(cls.Class), string(cls.Decision), string(cls.Confidence), cls.Reason, rec.Summary)
	if err != nil {
		return 0, fmt.Errorf("record qg failure occurrence: insert incident: %w", err)
	}
	incidentID, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("record qg failure occurrence: incident id: %w", err)
	}
	return incidentID, nil
}

func fetchQGIncident(ctx context.Context, tx *sql.Tx, id int64) (QGIncident, error) {
	var incident QGIncident
	var class, decision, confidence string
	err := tx.QueryRowContext(ctx, `
SELECT id, fingerprint, class, decision, confidence, reason, summary, status, occurrence_count
  FROM qg_failure_incidents
 WHERE id=?`, id).Scan(
		&incident.ID,
		&incident.Fingerprint,
		&class,
		&decision,
		&confidence,
		&incident.Reason,
		&incident.Summary,
		&incident.Status,
		&incident.OccurrenceCount,
	)
	if err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: fetch incident: %w", err)
	}
	incident.Class = QGFailureClass(class)
	incident.Decision = QGFailureDecision(decision)
	incident.Confidence = QGFailureConfidence(confidence)
	return incident, nil
}

func hashQGFailureOutput(output string) string {
	sum := sha256.Sum256([]byte(output))
	return hex.EncodeToString(sum[:])
}

func qgOccurrenceID(rec QGFailureRecord) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", rec.Fingerprint, rec.BeadID, rec.WorkerID, rec.AssignmentID, rec.OutputHash)))
	return hex.EncodeToString(sum[:])
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
