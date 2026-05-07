package dispatcher //nolint:testpackage // exercises unexported linkQGFailureToBeads acceptance signature

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

func TestQGFailureNotesLinkAffectedBeadsToIncident(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:       "oro-original",
		Title:    "original failed bead",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("create original: %v", err)
	}
	if err := store.Close(ctx, "oro-original", "already done"); err != nil {
		t.Fatalf("close original: %v", err)
	}

	d := &Dispatcher{beads: store}
	incident := QGIncident{
		ID:          42,
		Fingerprint: "qg:fingerprint",
		Class:       QGFailureClassSystemic,
		Decision:    QGFailureDecisionCreateOrReuseInfra,
		Confidence:  QGFailureConfidenceHigh,
		Summary:     "shared build failure",
		Status:      "open",
	}
	rec := QGFailureRecord{
		BeadID:      "oro-original",
		WorkerID:    "worker-a",
		Fingerprint: incident.Fingerprint,
		Summary:     incident.Summary,
		Output:      strings.Repeat("large qg output\n", 200),
		OutputHash:  "hash-123",
	}
	cls := QGFailureClassification{
		Class:      incident.Class,
		Decision:   incident.Decision,
		Confidence: incident.Confidence,
		Reason:     "same failure across beads",
	}

	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		t.Fatalf("link first: %v", err)
	}
	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		t.Fatalf("link duplicate: %v", err)
	}

	original, err := store.Show(ctx, "oro-original")
	if err != nil {
		t.Fatalf("show original: %v", err)
	}
	if original.Status != "closed" {
		t.Fatalf("original status = %q, want closed", original.Status)
	}
	for _, want := range []string{
		"qg_incident: 42",
		"class: systemic",
		"fingerprint: qg:fingerprint",
		"output_hash: hash-123",
	} {
		if !strings.Contains(original.Notes, want) {
			t.Fatalf("original notes missing %q:\n%s", want, original.Notes)
		}
	}

	infra, err := store.Show(ctx, "oro-qg-incident-42")
	if err != nil {
		t.Fatalf("show infra: %v", err)
	}
	if infra == nil {
		t.Fatal("infra incident bead was not created")
	}
	if infra.Type != "bug" || infra.Priority != 0 {
		t.Fatalf("infra bead type/priority = %s/%d, want bug/0", infra.Type, infra.Priority)
	}
	if got := strings.Count(infra.Notes, "affected_bead: oro-original"); got != 1 {
		t.Fatalf("affected bead note count = %d, want 1:\n%s", got, infra.Notes)
	}
}

func TestQGFailureReopensClosedIncidentBeadForAssignment(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:       "oro-affected",
		Title:    "affected bead",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("create affected: %v", err)
	}
	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:       "oro-qg-incident-7",
		Title:    "existing incident",
		Type:     "bug",
		Priority: 0,
	}); err != nil {
		t.Fatalf("create incident: %v", err)
	}
	if err := store.Close(ctx, "oro-qg-incident-7", "operator resolved prior occurrence"); err != nil {
		t.Fatalf("close incident: %v", err)
	}

	d := &Dispatcher{beads: store}
	incident := QGIncident{
		ID:          7,
		Fingerprint: "qg:shared",
		Class:       QGFailureClassSystemic,
		Decision:    QGFailureDecisionCreateOrReuseInfra,
		Confidence:  QGFailureConfidenceHigh,
		Summary:     "shared QG failure",
		Status:      "open",
	}
	rec := QGFailureRecord{
		BeadID:      "oro-affected",
		WorkerID:    "worker-a",
		Fingerprint: incident.Fingerprint,
		Summary:     incident.Summary,
		Output:      "same systemic failure",
		OutputHash:  "hash-7",
	}
	cls := QGFailureClassification{
		Class:      incident.Class,
		Decision:   incident.Decision,
		Confidence: incident.Confidence,
		Reason:     "same systemic failure recurred",
	}

	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		t.Fatalf("link recurring incident: %v", err)
	}

	infra, err := store.Show(ctx, "oro-qg-incident-7")
	if err != nil {
		t.Fatalf("show incident: %v", err)
	}
	if infra.Status != "open" {
		t.Fatalf("incident status = %q, want open", infra.Status)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if readyIDs(ready) != "oro-qg-incident-7,oro-affected" {
		t.Fatalf("Ready ids = %s, want reopened incident first", readyIDs(ready))
	}
}

func readyIDs(beads []protocol.Bead) string {
	ids := make([]string, 0, len(beads))
	for _, bead := range beads {
		ids = append(ids, bead.ID)
	}
	return strings.Join(ids, ",")
}
