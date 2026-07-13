package dispatcher //nolint:testpackage // pins shared janitor/audit suppression derivation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestJanitorSuppressionDerivation(t *testing.T) {
	assertDeriveSuppressedSignature((*Dispatcher).deriveSuppressed)

	d, store, _, _, _, _ := newTestDispatcher(t)
	roleIDs := []string{"oro-janitor-role", "oro-audit-role"}
	store.journeys = make(map[string][]beadstore.JourneyEvent)
	store.metadataMatches = []*protocol.Bead{
		{ID: "open", Status: "open", Metadata: map[string]any{auditFindingMetadataKey: "open"}},
		{ID: "fixed", Status: "closed", CloseReason: "", Metadata: map[string]any{auditFindingMetadataKey: "fixed"}},
	}
	appendSuppressionFixture(t, store, roleIDs[0], ops.Finding{ID: "open", Title: "open finding"}, "janitor_finding")
	appendSuppressionFixture(t, store, roleIDs[1], ops.Finding{ID: "fixed", Title: "fixed finding"}, "audit_finding")

	for i := range 60 {
		finding := ops.Finding{
			ID:       fmt.Sprintf("suppressed-%02d", i),
			Title:    fmt.Sprintf("suppressed finding %02d", i),
			Evidence: []ops.Evidence{{File: "audit.go", LineStart: 10 + i, LineEnd: 10 + i}},
		}
		reason := "wont-fix: intentional"
		roleID := roleIDs[i%len(roleIDs)]
		event := "janitor_finding"
		if roleID == roleIDs[1] {
			reason = "WONT-FIX: accepted risk"
			event = "audit_finding"
		}
		store.metadataMatches = append(store.metadataMatches, &protocol.Bead{
			ID:          fmt.Sprintf("oro-finding-%02d", i),
			Status:      "closed",
			CloseReason: reason,
			Metadata:    map[string]any{auditFindingMetadataKey: finding.ID},
		})
		appendSuppressionFixture(t, store, roleID, finding, event)
	}

	suppressed, err := d.deriveSuppressed(t.Context(), roleIDs)
	if err != nil {
		t.Fatalf("derive suppressed findings: %v", err)
	}
	if len(suppressed) != 60 {
		t.Fatalf("suppressed findings = %d, want all 60 closed wont-fix beads", len(suppressed))
	}
	for _, finding := range suppressed {
		if finding.Status != "wont-fix" {
			t.Fatalf("suppressed finding %q status = %q, want wont-fix", finding.ID, finding.Status)
		}
	}

	prior := suppressed[0]
	incoming := prior
	incoming.ID = "line-drifted-id"
	incoming.Evidence = []ops.Evidence{{
		File:      prior.Evidence[0].File,
		LineStart: prior.Evidence[0].LineStart + 3,
		LineEnd:   prior.Evidence[0].LineEnd + 3,
	}}
	if !findingSuppressed(incoming, suppressed) {
		t.Fatal("finding with three-line evidence drift was not bucket-suppressed")
	}

	t.Run("production janitor filing consults shared suppression", func(t *testing.T) {
		assertJanitorFilingUsesSharedSuppression(t)
	})
}

func assertDeriveSuppressedSignature(_ func(*Dispatcher, context.Context, []string) ([]ops.Finding, error)) {
}

func assertJanitorFilingUsesSharedSuppression(t *testing.T) {
	t.Helper()
	d, store, _, _, _, _ := newTestDispatcher(t)
	store.journeys = make(map[string][]beadstore.JourneyEvent)
	prior := ops.Finding{
		ID:       "audit-prior",
		Severity: ops.SevCritical,
		Title:    "shared cleanliness issue",
		Evidence: []ops.Evidence{{File: "audit.go", LineStart: 10, LineEnd: 10}},
	}
	incoming := prior
	incoming.ID = "janitor-line-drifted"
	incoming.Evidence = []ops.Evidence{{File: "audit.go", LineStart: 13, LineEnd: 13}}
	eligible := ops.Finding{
		ID:       "janitor-eligible",
		Severity: ops.SevMinor,
		Title:    "eligible janitor issue",
		Sources:  []string{"todo"},
	}
	store.metadataMatches = []*protocol.Bead{
		{ID: "oro-audit-role", Status: "closed", Metadata: map[string]any{cleanlinessRoleMetadataKey: "audit"}},
		{ID: "oro-janitor-role", Status: "closed", Metadata: map[string]any{cleanlinessRoleMetadataKey: "janitor"}},
		{
			ID:          "oro-suppressed",
			Status:      "closed",
			CloseReason: "wont-fix: accepted audit risk",
			Metadata:    map[string]any{auditFindingMetadataKey: prior.ID},
		},
	}
	appendSuppressionFixture(t, store, "oro-audit-role", prior, "audit_finding")
	feedback, err := json.Marshal(janitorResultPayload{
		Findings:     []ops.Finding{incoming, eligible},
		RanDetectors: []string{"todo"},
	})
	if err != nil {
		t.Fatalf("marshal janitor suppression result: %v", err)
	}

	d.handleJanitorResult(t.Context(), ops.Result{
		Type: ops.OpsJanitor, BeadID: "oro-janitor-role", Feedback: string(feedback),
	})

	created := createdCallsWithMetadata(store, auditFindingMetadataKey, "")
	if len(created) != 1 || created[0].title != eligible.Title {
		t.Fatalf("janitor filed findings = %#v, want only eligible finding", created)
	}
}

func appendSuppressionFixture(t *testing.T, store *fakeBeadStore, roleID string, finding ops.Finding, event string) {
	t.Helper()
	payload, err := json.Marshal(finding)
	if err != nil {
		t.Fatalf("marshal suppression fixture: %v", err)
	}
	store.journeys[roleID] = append(store.journeys[roleID], beadstore.JourneyEvent{
		Actor:   roleActorForEvent(event),
		Event:   event,
		Payload: string(payload),
	})
}

func roleActorForEvent(event string) string {
	if event == "audit_finding" {
		return auditRoleActor
	}
	return janitorRoleActor
}
