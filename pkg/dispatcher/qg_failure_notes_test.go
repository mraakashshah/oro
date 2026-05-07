package dispatcher //nolint:testpackage // exercises unexported linkQGFailureToBeads acceptance signature

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
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
