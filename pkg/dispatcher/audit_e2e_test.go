package dispatcher //nolint:testpackage // exercises the internal audit lifecycle with a real bead store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestAuditCycleEndToEnd(t *testing.T) {
	ctx := context.Background()
	d, _, worktrees, _, _, spawner := newTestDispatcher(t)
	db := newTestDB(t)
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate fixture bead schema: %v", err)
	}
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("migrate fixture journey schema: %v", err)
	}
	store := beadstore.NewSQLiteStore(db)
	d.beads = store
	d.cfg.DefaultBranch = "main"
	d.cfg.JanitorEnabled = true
	d.cfg.JanitorInterval = 1
	d.cfg.JanitorIdleThreshold = 0
	d.cfg.AuditEnabled = true
	d.cfg.AuditEveryNJanitors = 5

	repo := auditE2EFixtureRepo(t)
	d.repoRoot = repo
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return repo, "agent/audit-scan", nil
	}
	spawner.verdict = auditE2ESectionOutput(t)

	var janitorRuns atomic.Int64
	d.janitorSpawnFn = func(context.Context) {
		janitorRuns.Add(1)
	}
	for range 5 {
		d.maybeTriggerJanitor(ctx)
	}

	waitFor(t, func() bool {
		return janitorRuns.Load() == 4 && eventCount(t, d.db, "audit_coverage") == 1
	}, 10*time.Second)
	if got := spawner.SpawnCount(); got != len(ops.AuditSectionIDs()) {
		t.Fatalf("audit section spawns = %d, want %d", got, len(ops.AuditSectionIDs()))
	}

	filed, err := store.FindByMetadataKey(ctx, auditFindingMetadataKey)
	if err != nil {
		t.Fatalf("find filed audit findings: %v", err)
	}
	if len(filed) != 3 {
		t.Fatalf("filed audit findings = %d, want three gated manifest-valid survivors: %#v", len(filed), filed)
	}
	wantPriorities := map[string]int{
		"critical manifest finding":   0,
		"important file-only finding": 1,
		"minor manifest finding":      2,
	}
	for _, finding := range filed {
		wantPriority, ok := wantPriorities[finding.Title]
		if !ok {
			t.Errorf("unexpected filed finding %q", finding.Title)
			continue
		}
		if finding.Priority != wantPriority {
			t.Errorf("finding %q priority = %d, want %d", finding.Title, finding.Priority, wantPriority)
		}
	}

	role := auditE2ERoleBead(ctx, t, store)
	journey, err := store.Journey(ctx, role.ID, time.Time{})
	if err != nil {
		t.Fatalf("audit role journey: %v", err)
	}
	assertAuditCoverageJourney(t, journey)
	belowGatePersisted := false
	for _, event := range journey {
		if event.Event != "audit_finding" {
			continue
		}
		var finding ops.Finding
		if err := json.Unmarshal([]byte(event.Payload), &finding); err != nil {
			t.Fatalf("parse audit finding journey: %v", err)
		}
		if finding.Title == "invalid manifest finding" {
			t.Fatalf("audit journey persisted a manifest-invalid finding: %#v", journey)
		}
		if finding.Title == "below gate finding" && finding.Status == "below_gate" {
			belowGatePersisted = true
		}
	}
	if !belowGatePersisted {
		t.Fatalf("audit journey omitted the validated below-gate finding: %#v", journey)
	}

	d.mu.Lock()
	mergesSinceJanitor := d.mergesSinceJanitor
	janitorRunsSinceAudit := d.janitorRunsSinceAudit
	d.mu.Unlock()
	if mergesSinceJanitor != 0 || janitorRunsSinceAudit != 0 {
		t.Fatalf("audit counters after fifth trigger = merges:%d janitors:%d, want both reset", mergesSinceJanitor, janitorRunsSinceAudit)
	}
}

func auditE2EFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	contents := "package fixture\n\nfunc manifestFinding() {}\n"
	if err := os.WriteFile(filepath.Join(repo, "audit.go"), []byte(contents), 0o600); err != nil {
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
	return repo
}

func auditE2ESectionOutput(t *testing.T) string {
	t.Helper()
	report := ops.ReviewReport{
		Verdict: ops.VerdictRejected,
		Findings: []ops.Finding{
			{
				Severity: ops.SevCritical, Title: "critical manifest finding", Detail: "critical detail",
				Evidence:   []ops.Evidence{{File: "audit.go", LineStart: 1, LineEnd: 1, Quote: "package fixture"}},
				Confidence: 50, Origin: "pre_existing",
			},
			{
				Severity: ops.SevImportant, Title: "important file-only finding", Detail: "important detail",
				Evidence: []ops.Evidence{{File: "audit.go"}}, Confidence: 50, Origin: "pre_existing",
			},
			{
				Severity: ops.SevMinor, Title: "minor manifest finding", Detail: "minor detail",
				Evidence:   []ops.Evidence{{File: "audit.go", LineStart: 3, LineEnd: 3, Quote: "func manifestFinding"}},
				Confidence: 50, Origin: "pre_existing",
			},
			{
				Severity: ops.SevMinor, Title: "below gate finding", Detail: "below gate detail",
				Evidence: []ops.Evidence{{File: "audit.go"}}, Confidence: 49, Origin: "pre_existing",
			},
			{
				Severity: ops.SevCritical, Title: "invalid manifest finding", Detail: "invalid manifest detail",
				Evidence:   []ops.Evidence{{File: "missing.go", LineStart: 1, LineEnd: 1, Quote: "missing"}},
				Confidence: 100, Origin: "pre_existing",
			},
		},
	}
	return auditSectionOutput(t, report)
}

func auditE2ERoleBead(ctx context.Context, t *testing.T, store beadstore.Store) *protocol.Bead {
	t.Helper()
	roles, err := store.FindByMetadataKey(ctx, cleanlinessRoleMetadataKey)
	if err != nil {
		t.Fatalf("find audit role: %v", err)
	}
	for _, role := range roles {
		if role.Metadata[cleanlinessRoleMetadataKey] == "audit" {
			return role
		}
	}
	t.Fatalf("audit role not found: %#v", roles)
	return nil
}

func eventPayloadTitle(payload string) string {
	var finding ops.Finding
	if json.Unmarshal([]byte(payload), &finding) != nil {
		return ""
	}
	return finding.Title
}
