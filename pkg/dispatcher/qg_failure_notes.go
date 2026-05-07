package dispatcher

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/beadstore"
)

const maxQGFailureNoteExcerptBytes = 1200

func (d *Dispatcher) linkQGFailureToBeads(ctx context.Context, incident QGIncident, rec QGFailureRecord, cls QGFailureClassification) error {
	if d.beads == nil {
		return fmt.Errorf("bead note API unavailable")
	}
	rec = normalizeQGFailureRecord(rec)

	if rec.BeadID != "" {
		note := formatOriginalQGFailureNote(incident, rec, cls)
		if err := d.appendUniqueBeadNote(ctx, rec.BeadID, note, fmt.Sprintf("qg_incident: %d", incident.ID)); err != nil {
			return err
		}
	}
	if cls.Decision != QGFailureDecisionCreateOrReuseInfra {
		return nil
	}

	infraID := qgIncidentBeadID(incident.ID)
	if err := d.ensureQGIncidentBead(ctx, infraID, incident, rec, cls); err != nil {
		return err
	}
	note := formatAffectedBeadQGFailureNote(incident, rec, cls)
	marker := fmt.Sprintf("affected_bead: %s", rec.BeadID)
	return d.appendUniqueBeadNote(ctx, infraID, note, marker)
}

func (d *Dispatcher) createOrReuseQGInfraIncident(ctx context.Context, rec QGFailureRecord, cls QGFailureClassification) (QGIncident, error) {
	rec = normalizeQGFailureRecord(rec)
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		return QGIncident{}, err
	}
	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_incident_create_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"incident_id":%d,"fingerprint":%q,"error":%q}`, incident.ID, rec.Fingerprint, err.Error()))
		if rec.BeadID != "" {
			_ = d.updateBeadStatus(ctx, rec.BeadID, "open")
		}
	}
	return incident, nil
}

func (d *Dispatcher) ensureQGIncidentBead(ctx context.Context, infraID string, incident QGIncident, rec QGFailureRecord, cls QGFailureClassification) error {
	existing, err := d.beads.Show(ctx, infraID)
	if err != nil {
		return fmt.Errorf("show qg incident bead %s: %w", infraID, err)
	}
	if existing != nil {
		return nil
	}

	desc := fmt.Sprintf("Infrastructure issue for QG incident %d.\n\nfingerprint: %s\nclass: %s\ndecision: %s\nconfidence: %s\nreason: %s",
		incident.ID, rec.Fingerprint, cls.Class, cls.Decision, cls.Confidence, cls.Reason)
	_, err = d.beads.Create(ctx, beadstore.CreateParams{
		ID:          infraID,
		Title:       fmt.Sprintf("QG incident %d: %s", incident.ID, incident.Summary),
		Type:        "bug",
		Priority:    0,
		Description: desc,
		Tags:        []string{"qg-failure", "infra"},
	})
	if err != nil {
		return fmt.Errorf("create qg incident bead %s: %w", infraID, err)
	}
	return nil
}

func (d *Dispatcher) appendUniqueBeadNote(ctx context.Context, beadID, note, marker string) error {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return fmt.Errorf("show bead %s: %w", beadID, err)
	}
	if detail == nil {
		return fmt.Errorf("show bead %s: not found", beadID)
	}
	if marker != "" && strings.Contains(detail.Notes, marker) {
		return nil
	}
	if err := d.beads.Update(ctx, beadID, beadstore.UpdateParams{Notes: &note}); err != nil {
		return fmt.Errorf("append qg note to %s: %w", beadID, err)
	}
	return nil
}

func formatOriginalQGFailureNote(incident QGIncident, rec QGFailureRecord, cls QGFailureClassification) string {
	return strings.TrimSpace(fmt.Sprintf(`QG failure recorded
qg_incident: %d
class: %s
decision: %s
confidence: %s
fingerprint: %s
output_hash: %s
summary: %s
reason: %s
output_excerpt:
%s`,
		incident.ID, cls.Class, cls.Decision, cls.Confidence, rec.Fingerprint, rec.OutputHash, rec.Summary, cls.Reason, qgFailureOutputExcerpt(rec.Output)))
}

func formatAffectedBeadQGFailureNote(incident QGIncident, rec QGFailureRecord, cls QGFailureClassification) string {
	return strings.TrimSpace(fmt.Sprintf(`QG affected bead
qg_incident: %d
affected_bead: %s
worker_id: %s
class: %s
fingerprint: %s
output_hash: %s
summary: %s`,
		incident.ID, rec.BeadID, rec.WorkerID, cls.Class, rec.Fingerprint, rec.OutputHash, rec.Summary))
}

func qgIncidentBeadID(incidentID int64) string {
	return fmt.Sprintf("oro-qg-incident-%d", incidentID)
}

func qgFailureOutputExcerpt(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= maxQGFailureNoteExcerptBytes {
		return output
	}
	return output[:maxQGFailureNoteExcerptBytes] + "..."
}
