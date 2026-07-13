package dispatcher //nolint:testpackage // verifies the internal audit lifecycle end-to-end

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestAuditSpawnMergePipeline(t *testing.T) {
	d, beads, worktrees, esc, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}

	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict: ops.VerdictRejected,
		Findings: []ops.Finding{{
			Severity:   ops.SevImportant,
			Category:   "correctness",
			Title:      "shared audit finding",
			Detail:     "the fixture needs a remediation",
			Evidence:   []ops.Evidence{{File: "audit.go", LineStart: 1, LineEnd: 1, Quote: "package fixture"}},
			Confidence: 50,
			Origin:     "pre_existing",
		}},
	})

	triggerAuditCycle(context.Background(), d)

	waitFor(t, func() bool {
		beads.mu.Lock()
		defer beads.mu.Unlock()
		return len(beads.created) == 1
	}, 2*time.Second)

	beads.mu.Lock()
	created := beads.created[0]
	beads.mu.Unlock()
	if created.priority != 1 {
		t.Fatalf("audit finding priority = %d, want 1", created.priority)
	}
	if created.beadType != "task" {
		t.Fatalf("audit finding type = %q, want task", created.beadType)
	}
	if created.title != "shared audit finding" {
		t.Fatalf("audit finding title = %q", created.title)
	}
	if !strings.Contains(created.description, "wont-fix:") || !strings.Contains(created.description, "reopen") {
		t.Fatalf("audit finding description omitted suppression contract: %q", created.description)
	}
	if got := spawner.SpawnCount(); got != 6 {
		t.Fatalf("audit spawn calls = %d, want six sections", got)
	}
	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("audit escalations = %d, want none", got)
	}
	var coveragePayload string
	if err := d.db.QueryRow(`SELECT payload FROM events WHERE type='audit_coverage' ORDER BY id DESC LIMIT 1`).Scan(&coveragePayload); err != nil {
		t.Fatalf("load audit coverage event: %v", err)
	}
	var coverage struct {
		CoveredSections []string `json:"covered_sections"`
		NotCovered      []string `json:"not_covered"`
	}
	if err := json.Unmarshal([]byte(coveragePayload), &coverage); err != nil {
		t.Fatalf("parse audit coverage event: %v", err)
	}
	wantCovered := []string{"code-quality", "tests-safety", "data-migrations", "security-static", "perf-patterns", "dx-deps-docs"}
	wantNotCovered := []string{"product-correctness-live", "reliability-injection", "integrations-live", "deploy-observability"}
	if !slices.Equal(coverage.CoveredSections, wantCovered) || !slices.Equal(coverage.NotCovered, wantNotCovered) {
		t.Fatalf("audit coverage = %#v, want covered=%#v not_covered=%#v", coverage, wantCovered, wantNotCovered)
	}

	d.mu.Lock()
	mergesSinceJanitor := d.mergesSinceJanitor
	janitorRunsSinceAudit := d.janitorRunsSinceAudit
	d.mu.Unlock()
	if mergesSinceJanitor != 0 || janitorRunsSinceAudit != 0 {
		t.Fatalf("audit altered counters: merges=%d janitors=%d", mergesSinceJanitor, janitorRunsSinceAudit)
	}

	t.Run("suppression matches janitor close semantics", func(t *testing.T) {
		finding := auditFixtureFinding()
		findingID := ops.FindingID("", finding)
		tests := []struct {
			name        string
			bead        *protocol.Bead
			wantCreated int
		}{
			{
				name: "open finding blocks duplicate filing",
				bead: auditFindingBead("open", "", findingID),
			},
			{
				name: "wont-fix close suppresses permanently",
				bead: auditFindingBead("closed", "wont-fix: intentional", findingID),
			},
			{
				name: "wont-fix prefix is case insensitive",
				bead: auditFindingBead("closed", "WONT-FIX: accepted risk", findingID),
			},
			{
				name:        "fixed close refiles when detected again",
				bead:        auditFindingBead("closed", "fixed", findingID),
				wantCreated: 1,
			},
			{
				name:        "reasonless close refiles when detected again",
				bead:        auditFindingBead("closed", "", findingID),
				wantCreated: 1,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assertAuditFindingCreateCount(t, finding, tc.bead, tc.wantCreated)
			})
		}
	})
}

func TestAuditSpawnAllSectionsFailedDoesNotEscalate(t *testing.T) {
	d, beads, worktrees, esc, _, spawner := newTestDispatcher(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return auditFixtureRepo(t), "agent/audit", nil
	}
	spawner.spawnErr = errors.New("audit runtime unavailable")

	triggerAuditCycle(context.Background(), d)
	waitFor(t, func() bool {
		return eventCount(t, d.db, "audit_failed") == 1
	}, 10*time.Second)

	beads.mu.Lock()
	created := len(beads.created)
	beads.mu.Unlock()
	if created != 0 {
		t.Fatalf("created beads = %d, want none", created)
	}
	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("audit escalations = %d, want none", got)
	}
	if got := eventCount(t, d.db, "audit_failed"); got != 1 {
		t.Fatalf("audit_failed notes = %d, want 1", got)
	}
	d.mu.Lock()
	mergesSinceJanitor := d.mergesSinceJanitor
	janitorRunsSinceAudit := d.janitorRunsSinceAudit
	d.mu.Unlock()
	if mergesSinceJanitor != 0 || janitorRunsSinceAudit != 0 {
		t.Fatalf("failed audit counters: merges=%d janitors=%d, want reset", mergesSinceJanitor, janitorRunsSinceAudit)
	}
}

func triggerAuditCycle(ctx context.Context, d *Dispatcher) {
	d.cfg.JanitorEnabled = true
	d.cfg.JanitorInterval = 1
	d.cfg.JanitorIdleThreshold = 0
	d.cfg.AuditEnabled = true
	d.cfg.AuditEveryNJanitors = 5
	d.mergesSinceJanitor = 0
	d.janitorRunsSinceAudit = 4
	d.maybeTriggerJanitor(ctx)
}

func assertAuditFindingCreateCount(t *testing.T, finding ops.Finding, existing *protocol.Bead, want int) {
	t.Helper()
	d, beads, worktrees, _, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}
	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict:  ops.VerdictRejected,
		Findings: []ops.Finding{finding},
	})
	beads.metadataMatches = []*protocol.Bead{existing}

	d.spawnAudit(context.Background(), nil)

	beads.mu.Lock()
	created := len(beads.created)
	beads.mu.Unlock()
	if created != want {
		t.Fatalf("created beads = %d, want %d", created, want)
	}
}

func auditFindingBead(status, closeReason, findingID string) *protocol.Bead {
	return &protocol.Bead{
		Status:      status,
		CloseReason: closeReason,
		Metadata: map[string]any{
			auditFindingMetadataKey: findingID,
		},
	}
}

func auditFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "audit.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write audit fixture: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "audit.go"},
		{"-c", "user.name=Oro Test", "-c", "user.email=oro@example.invalid", "commit", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if status := strings.TrimSpace(string(output)); status != "" {
		t.Fatalf("audit fixture is not a clean checkout: %s", status)
	}
	return repo
}

func auditSectionOutput(t *testing.T, report ops.ReviewReport) string {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal audit report: %v", err)
	}
	return "```json\n" + string(payload) + "\n```\nVERDICT: REJECTED\n"
}

func auditFixtureFinding() ops.Finding {
	return ops.Finding{
		Severity:   ops.SevImportant,
		Category:   "correctness",
		Title:      "shared audit finding",
		Detail:     "the fixture needs a remediation",
		Evidence:   []ops.Evidence{{File: "audit.go", LineStart: 1, LineEnd: 1, Quote: "package fixture"}},
		Confidence: 50,
		Origin:     "pre_existing",
	}
}
