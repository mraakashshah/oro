package dispatcher //nolint:testpackage // verifies the internal audit lifecycle end-to-end

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

	d.spawnAudit(context.Background(), nil)

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
	if got := spawner.SpawnCount(); got != 6 {
		t.Fatalf("audit spawn calls = %d, want six sections", got)
	}
	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("audit escalations = %d, want none", got)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mergesSinceJanitor != 0 || d.janitorRunsSinceAudit != 0 {
		t.Fatalf("audit altered counters: merges=%d janitors=%d", d.mergesSinceJanitor, d.janitorRunsSinceAudit)
	}
}

func TestAuditSpawnAllSectionsFailedDoesNotEscalate(t *testing.T) {
	d, beads, worktrees, esc, _, spawner := newTestDispatcher(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return auditFixtureRepo(t), "agent/audit", nil
	}
	spawner.spawnErr = errors.New("audit runtime unavailable")

	d.spawnAudit(context.Background(), nil)

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
}

func TestAuditSpawnSuppressesClosedFinding(t *testing.T) {
	d, beads, worktrees, _, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}
	finding := auditFixtureFinding()
	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict:  ops.VerdictRejected,
		Findings: []ops.Finding{finding},
	})
	beads.metadataMatches = []*protocol.Bead{{
		Status: "closed",
		Metadata: map[string]any{
			auditFindingMetadataKey: ops.FindingID("", finding),
		},
	}}

	d.spawnAudit(context.Background(), nil)

	beads.mu.Lock()
	created := len(beads.created)
	beads.mu.Unlock()
	if created != 0 {
		t.Fatalf("created beads = %d, want closed finding suppression", created)
	}
}

func auditFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "audit.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write audit fixture: %v", err)
	}
	for _, args := range [][]string{{"init"}, {"add", "audit.go"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
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
