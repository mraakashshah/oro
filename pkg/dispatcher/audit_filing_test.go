//nolint:testpackage // white-box test exercises the unexported audit result handler
package dispatcher

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
)

func TestAuditPartialCoverage(t *testing.T) {
	tests := []struct {
		name        string
		feedback    string
		wantCovered []string
	}{
		{
			name:        "partial unordered coverage is canonicalized",
			feedback:    `{"findings":[],"covered_sections":["tests-safety","unknown","code-quality","tests-safety"]}`,
			wantCovered: []string{"code-quality", "tests-safety"},
		},
		{
			name:     "missing coverage means no static section was covered",
			feedback: `{"findings":[]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, store, _, _, _, _ := newTestDispatcher(t)
			const roleBeadID = "oro-audit-role"
			d.handleAuditResult(t.Context(), ops.Result{
				Type: ops.OpsAudit, BeadID: roleBeadID, Feedback: tc.feedback,
			})

			store.mu.Lock()
			journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
			store.mu.Unlock()
			coverageEvents := make([]beadstore.JourneyEvent, 0, 1)
			for _, event := range journey {
				if event.Actor == auditRoleActor && event.Event == "audit_coverage" {
					coverageEvents = append(coverageEvents, event)
				}
			}
			if len(coverageEvents) != 1 {
				t.Fatalf("audit coverage events = %d, want 1: %#v", len(coverageEvents), journey)
			}
			var coverage struct {
				CoveredSections []string `json:"covered_sections"`
				NotCovered      []string `json:"not_covered"`
			}
			if err := json.Unmarshal([]byte(coverageEvents[0].Payload), &coverage); err != nil {
				t.Fatalf("parse audit coverage: %v", err)
			}
			if !slices.Equal(coverage.CoveredSections, tc.wantCovered) {
				t.Errorf("covered sections = %#v, want %#v", coverage.CoveredSections, tc.wantCovered)
			}
			wantNotCovered := append([]string(nil), ops.AuditSectionIDs()[len(tc.wantCovered):]...)
			wantNotCovered = append(wantNotCovered,
				"product-correctness-live",
				"reliability-injection",
				"integrations-live",
				"deploy-observability",
			)
			if !slices.Equal(coverage.NotCovered, wantNotCovered) {
				t.Errorf("not covered = %#v, want %#v", coverage.NotCovered, wantNotCovered)
			}
		})
	}
}

func TestAuditFilingAllSurvivors(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	const roleBeadID = "oro-audit-role"
	findings := []ops.Finding{
		{ID: "critical", Severity: ops.SevCritical, Title: "critical finding", Detail: "critical detail", Evidence: auditQuotedEvidence("critical")},
		{ID: "important", Severity: ops.SevImportant, Title: "important finding", Detail: "important detail", Evidence: auditQuotedEvidence("important")},
		{ID: "minor-1", Severity: ops.SevMinor, Title: "minor finding one", Detail: "minor detail", Evidence: auditQuotedEvidence("minor one")},
		{ID: "minor-2", Severity: ops.SevMinor, Title: "minor finding two", Detail: "minor detail", Evidence: auditQuotedEvidence("minor two")},
		{ID: "minor-3", Severity: ops.SevMinor, Title: "minor finding three", Detail: "minor detail", Evidence: auditQuotedEvidence("minor three")},
		{ID: "minor-4", Severity: ops.SevMinor, Title: "minor finding four", Detail: "minor detail", Evidence: auditQuotedEvidence("minor four")},
		{ID: "minor-5", Severity: ops.SevMinor, Title: "minor finding five", Detail: "minor detail", Evidence: auditQuotedEvidence("minor five")},
	}
	feedback, err := json.Marshal(auditResultPayload{
		Findings: findings, CoveredSections: ops.AuditSectionIDs(),
	})
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}

	d.handleAuditResult(ctx, ops.Result{Type: ops.OpsAudit, BeadID: roleBeadID, Feedback: string(feedback)})

	store.mu.Lock()
	created := append([]createCall(nil), store.created...)
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	if len(created) != len(findings) {
		t.Fatalf("created beads = %d, want every survivor (%d): %#v", len(created), len(findings), created)
	}
	priorities := map[string]int{"critical finding": 0, "important finding": 1}
	for _, call := range created {
		wantPriority, ok := priorities[call.title]
		if !ok {
			wantPriority = 2
		}
		if call.priority != wantPriority || call.beadType != "task" {
			t.Errorf("created bead %q = priority %d type %q, want priority %d task", call.title, call.priority, call.beadType, wantPriority)
		}
		if call.metadata[auditFindingMetadataKey] == "" {
			t.Errorf("created bead %q missing %s: %#v", call.title, auditFindingMetadataKey, call.metadata)
		}
		if !strings.Contains(call.description, "wont-fix:") || !strings.Contains(call.description, "reopen") {
			t.Errorf("created bead %q missing wont-fix contract: %q", call.title, call.description)
		}
	}
	assertAuditCoverageJourney(t, journey)
}

func TestAuditFilingAcceptance(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	findings := []ops.Finding{
		{ID: "audit-critical", Severity: ops.SevCritical, Title: "critical finding", Detail: "critical detail", Evidence: auditQuotedEvidence("critical")},
		{ID: "audit-empty-detail", Severity: ops.SevMinor, Title: "finding without detail", Evidence: auditQuotedEvidence("minor")},
	}

	d.fileAuditFindings(ctx, "oro-audit-role", findings, nil, nil)

	store.mu.Lock()
	created := append([]createCall(nil), store.created...)
	store.mu.Unlock()
	if len(created) != len(findings) {
		t.Fatalf("created beads = %d, want %d: %#v", len(created), len(findings), created)
	}
	for i, call := range created {
		lines := strings.Split(call.acceptanceCriteria, "\n")
		for _, prefix := range []string{"Test: ", "Cmd: ", "Assert: "} {
			if !slices.ContainsFunc(lines, func(line string) bool {
				return strings.HasPrefix(line, prefix) && strings.TrimSpace(strings.TrimPrefix(line, prefix)) != ""
			}) {
				t.Errorf("created bead %q acceptance missing non-empty %sline: %q", call.title, prefix, call.acceptanceCriteria)
			}
		}
		if !strings.Contains(call.acceptanceCriteria, "./scripts/quality_gate.sh") {
			t.Errorf("created bead %q acceptance does not invoke repository quality gate: %q", call.title, call.acceptanceCriteria)
		}
		if !strings.Contains(call.acceptanceCriteria, findings[i].ID) {
			t.Errorf("created bead %q acceptance missing finding ID %q: %q", call.title, findings[i].ID, call.acceptanceCriteria)
		}
	}
}

func auditQuotedEvidence(quote string) []ops.Evidence {
	return []ops.Evidence{{File: "audit.go", LineStart: 1, LineEnd: 1, Quote: quote}}
}

func TestAuditFindingAcceptanceEvidence(t *testing.T) {
	worktree := auditAcceptanceFixture(t)
	quotedPath := "code; touch path-injected.go"
	quotedLiteral := "unsafe ' $(touch quote-injected)"
	writeAuditAcceptanceFile(t, worktree, quotedPath, "package fixture\n// "+quotedLiteral+"\n")
	writeAuditAcceptanceFile(t, worktree, "whole.go", "package baseline\n")

	quoted := auditAcceptanceFinding("quoted", ops.Evidence{
		File: quotedPath, LineStart: 2, LineEnd: 2, Quote: quotedLiteral,
	})
	quotedParams, err := auditFindingCreateParams(quoted, worktree)
	if err != nil {
		t.Fatalf("quoted auditFindingCreateParams() error = %v", err)
	}
	quotedCmd := auditAcceptanceCommand(t, quotedParams.AcceptanceCriteria)
	assertAuditAcceptanceResult(t, worktree, quotedCmd, false)
	writeAuditAcceptanceFile(t, worktree, quotedPath, "package fixture\n// remediated\n")
	assertAuditAcceptanceResult(t, worktree, quotedCmd, true)
	assertAuditAcceptanceNotInjected(t, worktree)

	fileOnly := auditAcceptanceFinding("file-only", ops.Evidence{File: "whole.go"})
	fileParams, err := auditFindingCreateParams(fileOnly, worktree)
	if err != nil {
		t.Fatalf("file-only auditFindingCreateParams() error = %v", err)
	}
	fileCmd := auditAcceptanceCommand(t, fileParams.AcceptanceCriteria)
	assertAuditAcceptanceResult(t, worktree, fileCmd, false)
	writeAuditAcceptanceFile(t, worktree, "whole.go", "package remediated\n")
	assertAuditAcceptanceResult(t, worktree, fileCmd, true)

	assertAuditAcceptanceDeterministic(t, worktree, quoted, fileOnly)
	assertAuditAcceptanceRejectsUnsafeEvidence(t, worktree)
}

func auditAcceptanceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeAuditAcceptanceFile(t, root, "scripts/quality_gate.sh", "#!/bin/sh\n: > quality-gate-ran\n")
	if err := os.Chmod(filepath.Join(root, "scripts/quality_gate.sh"), 0o700); err != nil {
		t.Fatalf("make quality gate executable: %v", err)
	}
	return root
}

func writeAuditAcceptanceFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func auditAcceptanceFinding(id string, evidence ...ops.Evidence) ops.Finding {
	return ops.Finding{
		ID: id, Severity: ops.SevImportant, Title: id + " finding", Detail: id + " detail", Evidence: evidence,
	}
}

func auditAcceptanceCommand(t *testing.T, acceptance string) string {
	t.Helper()
	for _, line := range strings.Split(acceptance, "\n") {
		if command, ok := strings.CutPrefix(line, "Cmd: "); ok {
			if !strings.HasSuffix(command, "./scripts/quality_gate.sh") {
				t.Fatalf("acceptance command does not end with quality gate: %q", command)
			}
			return command
		}
	}
	t.Fatalf("acceptance criteria missing command: %q", acceptance)
	return ""
}

func assertAuditAcceptanceResult(t *testing.T, root, command string, wantPass bool) {
	t.Helper()
	_ = os.Remove(filepath.Join(root, "quality-gate-ran"))
	cmd := exec.Command("sh", "-c", command) //nolint:gosec // exercises generated acceptance in an isolated fixture.
	cmd.Dir = root
	err := cmd.Run()
	if (err == nil) != wantPass {
		t.Fatalf("acceptance command pass = %v, want %v; error = %v; command = %q", err == nil, wantPass, err, command)
	}
	_, markerErr := os.Stat(filepath.Join(root, "quality-gate-ran"))
	if (markerErr == nil) != wantPass {
		t.Fatalf("quality gate ran = %v, want %v", markerErr == nil, wantPass)
	}
}

func assertAuditAcceptanceNotInjected(t *testing.T, root string) {
	t.Helper()
	for _, marker := range []string{"path-injected.go", "quote-injected"} {
		if _, err := os.Stat(filepath.Join(root, marker)); !os.IsNotExist(err) {
			t.Fatalf("shell injection created %q: %v", marker, err)
		}
	}
}

func assertAuditAcceptanceDeterministic(t *testing.T, root string, quoted, fileOnly ops.Finding) {
	t.Helper()
	first := quoted
	first.Evidence = []ops.Evidence{quoted.Evidence[0], fileOnly.Evidence[0], quoted.Evidence[0]}
	second := quoted
	second.Evidence = []ops.Evidence{fileOnly.Evidence[0], quoted.Evidence[0]}
	firstParams, firstErr := auditFindingCreateParams(first, root)
	secondParams, secondErr := auditFindingCreateParams(second, root)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("deterministic params errors = %v, %v", firstErr, secondErr)
	}
	if firstCmd, secondCmd := auditAcceptanceCommand(t, firstParams.AcceptanceCriteria), auditAcceptanceCommand(t, secondParams.AcceptanceCriteria); firstCmd != secondCmd {
		t.Fatalf("acceptance commands differ after reorder/dedup:\n%s\n%s", firstCmd, secondCmd)
	}
}

func assertAuditAcceptanceRejectsUnsafeEvidence(t *testing.T, root string) {
	t.Helper()
	for name, evidence := range map[string][]ops.Evidence{
		"empty":     nil,
		"traversal": {{File: "../outside.go", Quote: "literal"}},
		"absolute":  {{File: filepath.Join(root, "whole.go"), Quote: "literal"}},
	} {
		t.Run(name, func(t *testing.T) {
			if params, err := auditFindingCreateParams(auditAcceptanceFinding(name, evidence...), root); err == nil {
				t.Fatalf("auditFindingCreateParams() = %#v, nil; want error", params)
			}
		})
	}
}

func TestAuditFilingZeroSurvivorsRecordsCoverage(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	const roleBeadID = "oro-audit-role"

	feedback, err := json.Marshal(auditResultPayload{CoveredSections: ops.AuditSectionIDs()})
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}
	d.handleAuditResult(ctx, ops.Result{Type: ops.OpsAudit, BeadID: roleBeadID, Feedback: string(feedback)})

	store.mu.Lock()
	created := len(store.created)
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	if created != 0 {
		t.Fatalf("created beads = %d, want zero", created)
	}
	assertAuditCoverageJourney(t, journey)
}

func assertAuditCoverageJourney(t *testing.T, journey []beadstore.JourneyEvent) {
	t.Helper()
	isCoverage := func(event beadstore.JourneyEvent) bool {
		return event.Actor == "ops_audit" && event.Event == "audit_coverage"
	}
	coverageIndex := slices.IndexFunc(journey, isCoverage)
	if coverageIndex < 0 {
		t.Fatalf("audit journey = %#v, want ops_audit audit_coverage", journey)
	}
	if slices.ContainsFunc(journey[coverageIndex+1:], isCoverage) {
		t.Fatalf("audit journey has duplicate coverage events: %#v", journey)
	}
	event := journey[coverageIndex]
	var coverage struct {
		CoveredSections []string `json:"covered_sections"`
		NotCovered      []string `json:"not_covered"`
	}
	if err := json.Unmarshal([]byte(event.Payload), &coverage); err != nil {
		t.Fatalf("parse coverage journey payload: %v", err)
	}
	if !slices.Equal(coverage.CoveredSections, ops.AuditSectionIDs()) {
		t.Errorf("covered sections = %#v, want %#v", coverage.CoveredSections, ops.AuditSectionIDs())
	}
	wantNotCovered := []string{"product-correctness-live", "reliability-injection", "integrations-live", "deploy-observability"}
	if !slices.Equal(coverage.NotCovered, wantNotCovered) {
		t.Errorf("not covered sections = %#v, want %#v", coverage.NotCovered, wantNotCovered)
	}
}
