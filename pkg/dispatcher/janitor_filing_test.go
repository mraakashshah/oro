//nolint:testpackage // white-box test exercises the unexported janitor result handler
package dispatcher

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestJanitorTopKConfig(t *testing.T) {
	const roleBeadID = "oro-janitor-role"
	findings := []ops.Finding{
		{ID: "critical-1", Severity: ops.SevCritical, Title: "critical one"},
		{ID: "suppressed", Severity: ops.SevCritical, Title: "suppressed critical"},
		{ID: "wont-fix", Severity: ops.SevCritical, Status: "wont-fix", Title: "wont-fix critical"},
		{ID: "critical-2", Severity: ops.SevCritical, Title: "critical two"},
		{ID: "important-1", Severity: ops.SevImportant, Title: "important one"},
		{ID: "important-2", Severity: ops.SevImportant, Title: "important two"},
		{ID: "important-3", Severity: ops.SevImportant, Title: "important three"},
		{ID: "minor-1", Severity: ops.SevMinor, Title: "minor one"},
		{ID: "minor-2", Severity: ops.SevMinor, Title: "minor two"},
		{ID: "minor-3", Severity: ops.SevMinor, Title: "minor three"},
		{ID: "minor-4", Severity: ops.SevMinor, Title: "minor four"},
		{ID: "minor-5", Severity: ops.SevMinor, Title: "minor five"},
	}
	wantOrder := []string{
		"critical one", "critical two",
		"important one", "important two", "important three",
		"minor one", "minor two", "minor three", "minor four", "minor five",
	}

	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{name: "configured two", limit: 2, want: 2},
		{name: "configured nine", limit: 9, want: 9},
		{name: "zero uses natural limit", limit: 0, want: janitorTopFindings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, store, _, _, _, _ := newTestDispatcher(t)
			d.cfg.JanitorTopK = tc.limit
			store.journeys = make(map[string][]beadstore.JourneyEvent)
			store.metadataMatches = []*protocol.Bead{
				{ID: roleBeadID, Status: "closed", Metadata: map[string]any{cleanlinessRoleMetadataKey: "janitor"}},
				{ID: "oro-existing", Status: "open", Metadata: map[string]any{auditFindingMetadataKey: "suppressed"}},
			}
			appendSuppressionFixture(t, store, roleBeadID, findings[1], "janitor_finding")

			feedback, err := json.Marshal(janitorResultPayload{Findings: findings})
			if err != nil {
				t.Fatalf("marshal janitor result: %v", err)
			}
			d.handleJanitorResult(t.Context(), ops.Result{
				Type: ops.OpsJanitor, BeadID: roleBeadID, Feedback: string(feedback),
			})

			store.mu.Lock()
			created := append([]createCall(nil), store.created...)
			store.mu.Unlock()
			if len(created) != tc.want {
				t.Fatalf("created beads = %d, want %d", len(created), tc.want)
			}
			for i, call := range created {
				if call.title != wantOrder[i] {
					t.Fatalf("created bead %d title = %q, want %q", i, call.title, wantOrder[i])
				}
			}
		})
	}

	original := append([]ops.Finding(nil), findings...)
	_ = janitorTopFindingsBySeverity(findings, 0)
	if !reflect.DeepEqual(findings, original) {
		t.Fatalf("janitor severity selection mutated input\n got: %#v\nwant: %#v", findings, original)
	}
}

func TestJanitorFilingTopFive(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	const roleBeadID = "oro-janitor-role"

	findings := []ops.Finding{
		{ID: "minor-1", Severity: ops.SevMinor, Title: "minor one", Detail: "minor detail", Sources: []string{"todo"}},
		{ID: "critical-1", Severity: ops.SevCritical, Title: "critical one", Detail: "critical detail", Sources: []string{"golangci-lint", "missing-tool"}},
		{ID: "important-1", Severity: ops.SevImportant, Title: "important one", Detail: "important detail", Sources: []string{"todo"}},
		{ID: "minor-2", Severity: ops.SevMinor, Title: "minor two", Detail: "minor detail", Sources: []string{"todo"}},
		{ID: "important-2", Severity: ops.SevImportant, Title: "important two", Detail: "important detail", Sources: []string{"golangci-lint"}},
		{ID: "minor-3", Severity: ops.SevMinor, Title: "minor three", Detail: "minor detail", Sources: []string{"todo"}},
		{ID: "minor-4", Severity: ops.SevMinor, Title: "minor four", Detail: "minor detail", Sources: []string{"todo"}},
	}
	feedback, err := json.Marshal(struct {
		Findings     []ops.Finding `json:"findings"`
		RanDetectors []string      `json:"ran_detectors"`
	}{Findings: findings, RanDetectors: []string{"todo", "golangci-lint"}})
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}

	d.handleJanitorResult(ctx, ops.Result{Type: ops.OpsJanitor, BeadID: roleBeadID, Feedback: string(feedback)})

	store.mu.Lock()
	created := append([]createCall(nil), store.created...)
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	if len(created) != 5 {
		t.Fatalf("created beads = %d, want 5: %#v", len(created), created)
	}
	if created[0].title != "critical one" || created[1].title != "important one" || created[2].title != "important two" {
		t.Fatalf("created severity ordering = [%q %q %q], want critical then important", created[0].title, created[1].title, created[2].title)
	}
	for _, call := range created {
		if call.priority != 2 || call.beadType != "task" {
			t.Errorf("created bead = priority %d type %q, want low-priority task", call.priority, call.beadType)
		}
		if call.metadata["meta_finding_id"] == "" {
			t.Errorf("created bead missing meta_finding_id: %#v", call.metadata)
		}
		if !strings.Contains(call.description, "wont-fix:") || !strings.Contains(call.description, "reopen") {
			t.Errorf("description missing wont-fix/reopen contract: %q", call.description)
		}
		if strings.Contains(call.acceptanceCriteria, "missing-tool") {
			t.Errorf("acceptance includes detector that did not run: %q", call.acceptanceCriteria)
		}
		if !strings.Contains(call.acceptanceCriteria, "Cmd:") {
			t.Errorf("acceptance missing rerun command: %q", call.acceptanceCriteria)
		}
	}
	if len(journey) != len(findings)+1 {
		t.Fatalf("janitor journey events = %d, want %d", len(journey), len(findings)+1)
	}
	for _, event := range journey[:len(findings)] {
		if event.Actor != "ops_janitor" || event.Event != "janitor_finding" {
			t.Errorf("journey finding event = %#v, want ops_janitor janitor_finding", event)
		}
	}
}

func TestJanitorFilingMalformedJSONRecordsJourneyNote(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	const roleBeadID = "oro-janitor-role"

	d.handleJanitorResult(ctx, ops.Result{Type: ops.OpsJanitor, BeadID: roleBeadID, Feedback: "not json"})

	store.mu.Lock()
	created := len(store.created)
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	if created != 0 {
		t.Fatalf("created beads = %d, want 0", created)
	}
	if len(journey) != 1 || journey[0].Actor != "ops_janitor" || journey[0].Event != "note" {
		t.Fatalf("malformed JSON journey = %#v, want one ops_janitor note", journey)
	}
}
