package dispatcher

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxQGFailureStoreAttempts = 8

type qgTargetObservation struct {
	passed              bool
	failureFingerprints map[string]struct{}
}

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

	var lastErr error
	for attempt := range maxQGFailureStoreAttempts {
		incident, err := recordQGFailureOccurrenceOnce(ctx, db, rec, cls)
		if err == nil {
			return incident, nil
		}
		lastErr = err
		if !isRetryableQGStoreError(err) || attempt == maxQGFailureStoreAttempts-1 {
			break
		}
		if err := waitQGStoreRetry(ctx, attempt); err != nil {
			return QGIncident{}, fmt.Errorf("record qg failure occurrence: retry: %w", err)
		}
	}
	return QGIncident{}, lastErr
}

type qgFailureStoreConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func recordQGFailureOccurrenceOnce(ctx context.Context, db *sql.DB, rec QGFailureRecord, cls QGFailureClassification) (QGIncident, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: busy timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	incident, err := recordQGFailureOccurrenceTx(ctx, conn, rec, cls)
	if err != nil {
		return QGIncident{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return QGIncident{}, fmt.Errorf("record qg failure occurrence: commit: %w", err)
	}
	committed = true
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

func recordQGFailureOccurrenceTx(ctx context.Context, conn qgFailureStoreConn, rec QGFailureRecord, cls QGFailureClassification) (QGIncident, error) {
	incidentID, err := ensureQGIncident(ctx, conn, rec, cls)
	if err != nil {
		return QGIncident{}, err
	}
	result, err := conn.ExecContext(ctx, `
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
		if _, err := conn.ExecContext(ctx, `
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

	incident, err := fetchQGIncident(ctx, conn, incidentID)
	if err != nil {
		return QGIncident{}, err
	}
	return incident, nil
}

func ensureQGIncident(ctx context.Context, conn qgFailureStoreConn, rec QGFailureRecord, cls QGFailureClassification) (int64, error) {
	var incidentID int64
	err := conn.QueryRowContext(ctx, `SELECT id FROM qg_failure_incidents WHERE fingerprint=?`, rec.Fingerprint).Scan(&incidentID)
	if err == nil {
		return incidentID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("record qg failure occurrence: query incident: %w", err)
	}

	result, err := conn.ExecContext(ctx, `
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

func fetchQGIncident(ctx context.Context, conn qgFailureStoreConn, id int64) (QGIncident, error) {
	var incident QGIncident
	var class, decision, confidence string
	err := conn.QueryRowContext(ctx, `
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

func (d *Dispatcher) classifyQGFailure(ctx context.Context, rec QGFailureRecord, override QGFailureHistory) QGFailureClassification {
	attribution := d.qgFailureAttribution(ctx, rec.WorkerID, rec)
	return d.classifyQGFailureWithAttribution(ctx, rec, override, attribution)
}

func (d *Dispatcher) classifyQGFailureWithAttribution(
	ctx context.Context,
	rec QGFailureRecord,
	override QGFailureHistory,
	attribution QGFailureAttribution,
) QGFailureClassification {
	history := d.loadQGFailureHistory(ctx, rec)
	history.KnownFlaky = history.KnownFlaky || override.KnownFlaky
	history.RerunPassed = history.RerunPassed || override.RerunPassed
	history.RetryExhausted = history.RetryExhausted || override.RetryExhausted
	if override.AffectedBeads > history.AffectedBeads {
		history.AffectedBeads = override.AffectedBeads
	}
	return ClassifyQGFailure(rec, history, attribution)
}

func (d *Dispatcher) qgFailureAttribution(ctx context.Context, workerID string, record QGFailureRecord) QGFailureAttribution {
	d.mu.Lock()
	worker := d.workers[workerID]
	if worker == nil {
		d.mu.Unlock()
		return QGFailureAttribution{}
	}
	worktree, targetSHA := worker.worktree, worker.targetSHA
	d.mu.Unlock()
	if worktree == "" || targetSHA == "" {
		return QGFailureAttribution{}
	}

	headOut, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil {
		return QGFailureAttribution{}
	}
	candidateSHA := strings.TrimSpace(string(headOut))
	attribution := QGFailureAttribution{CandidateSHA: candidateSHA, TargetSHA: targetSHA}
	if candidateSHA == "" {
		return attribution
	}
	if candidateSHA == targetSHA {
		d.recordQGTargetFailure(targetSHA, record.Fingerprint)
		attribution.TargetKnown = true
		attribution.TargetFingerprint = record.Fingerprint
		return attribution
	}

	d.mu.Lock()
	observation, ok := d.qgTargetObservations[targetSHA]
	_, fingerprintObserved := observation.failureFingerprints[record.Fingerprint]
	d.mu.Unlock()
	if !observation.passed && d.acceptedQGTargetPassed(ctx, targetSHA) {
		d.recordQGTargetPass(targetSHA)
		observation.passed = true
		ok = true
	}
	if !ok {
		return attribution
	}
	attribution.TargetKnown = true
	attribution.TargetPassed = observation.passed
	if fingerprintObserved && record.Fingerprint != "" {
		attribution.TargetFingerprint = record.Fingerprint
	}
	return attribution
}

func (d *Dispatcher) acceptedQGTargetPassed(ctx context.Context, targetSHA string) bool {
	if d.db == nil || targetSHA == "" {
		return false
	}
	var matches int
	err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM review_checkpoints
WHERE state = ? AND head_sha = ? AND target_sha = ?`,
		ReviewCheckpointStateQGPassed, targetSHA, targetSHA).Scan(&matches)
	return err == nil && matches > 0
}

func (d *Dispatcher) recordQGTargetPass(targetSHA string) {
	if targetSHA == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.qgTargetObservations == nil {
		d.qgTargetObservations = make(map[string]qgTargetObservation)
	}
	observation := d.qgTargetObservations[targetSHA]
	observation.passed = true
	d.qgTargetObservations[targetSHA] = observation
}

func (d *Dispatcher) recordQGTargetFailure(targetSHA, fingerprint string) {
	if targetSHA == "" || fingerprint == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.qgTargetObservations == nil {
		d.qgTargetObservations = make(map[string]qgTargetObservation)
	}
	observation := d.qgTargetObservations[targetSHA]
	if observation.failureFingerprints == nil {
		observation.failureFingerprints = make(map[string]struct{})
	}
	observation.failureFingerprints[fingerprint] = struct{}{}
	d.qgTargetObservations[targetSHA] = observation
}

func (d *Dispatcher) loadQGFailureHistory(ctx context.Context, rec QGFailureRecord) QGFailureHistory {
	if d == nil || d.db == nil || rec.Fingerprint == "" {
		return QGFailureHistory{}
	}

	var affectedBeads int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT o.bead_id)
  FROM qg_failure_occurrences o
  JOIN qg_failure_incidents i ON i.id = o.incident_id
 WHERE i.fingerprint = ?
   AND o.bead_id IS NOT NULL
   AND o.bead_id != ''`,
		rec.Fingerprint).Scan(&affectedBeads); err != nil {
		_ = d.logEvent(ctx, "qg_failure_history_load_failed", "dispatcher", rec.BeadID, rec.WorkerID, err.Error())
		return QGFailureHistory{}
	}
	if rec.BeadID != "" {
		var currentSeen int
		if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM qg_failure_occurrences o
  JOIN qg_failure_incidents i ON i.id = o.incident_id
 WHERE i.fingerprint = ?
   AND o.bead_id = ?`,
			rec.Fingerprint, rec.BeadID).Scan(&currentSeen); err != nil {
			_ = d.logEvent(ctx, "qg_failure_history_load_failed", "dispatcher", rec.BeadID, rec.WorkerID, err.Error())
			return QGFailureHistory{AffectedBeads: affectedBeads}
		}
		if currentSeen == 0 {
			affectedBeads++
		}
	}

	var flakyRows int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM qg_failure_incidents
 WHERE fingerprint = ?
   AND (class = ? OR decision = ?)`,
		rec.Fingerprint, string(QGFailureClassFlaky), string(QGFailureDecisionBackoffRetry)).Scan(&flakyRows); err != nil {
		_ = d.logEvent(ctx, "qg_failure_history_load_failed", "dispatcher", rec.BeadID, rec.WorkerID, err.Error())
	}

	return QGFailureHistory{
		AffectedBeads: affectedBeads,
		KnownFlaky:    flakyRows > 0,
	}
}

func isRetryableQGStoreError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "sqlite_busy") ||
		strings.Contains(text, "database is locked") ||
		strings.Contains(text, "database table is locked") ||
		strings.Contains(text, "unique constraint failed")
}

func waitQGStoreRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * 10 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("context done: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
