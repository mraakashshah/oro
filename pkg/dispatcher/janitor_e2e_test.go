package dispatcher //nolint:testpackage // exercises the internal janitor lifecycle with a real bead store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/protocol"
)

func TestJanitorCycleEndToEnd(t *testing.T) {
	ctx := context.Background()
	d, _, worktrees, _, _, _ := newTestDispatcher(t)
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

	repo := janitorFixtureRepo(t)
	d.repoRoot = repo
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return repo, "agent/janitor-scan", nil
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(gitPath))

	if err := d.runJanitor(ctx); err != nil {
		t.Fatalf("first janitor cycle: %v", err)
	}

	filed := janitorFiledFindings(ctx, t, store)
	if len(filed) != 1 {
		t.Fatalf("filed findings = %d, want 1", len(filed))
	}
	first := filed[0]
	if first.Metadata[janitorFindingMetadataKey] == "" {
		t.Fatalf("filed finding metadata = %#v, want meta_finding_id", first.Metadata)
	}
	if !strings.Contains(first.AcceptanceCriteria, "--detector todo") {
		t.Fatalf("acceptance = %q, want todo detector re-run", first.AcceptanceCriteria)
	}
	if strings.Contains(first.AcceptanceCriteria, "deadcode") {
		t.Fatalf("acceptance = %q, must not embed skipped deadcode detector", first.AcceptanceCriteria)
	}

	if err := store.Close(ctx, first.ID, "wont-fix: intentional fixture TODO"); err != nil {
		t.Fatalf("close through real CLI store path: %v", err)
	}
	if err := d.runJanitor(ctx); err != nil {
		t.Fatalf("second janitor cycle: %v", err)
	}
	if got := len(janitorFiledFindings(ctx, t, store)); got != 1 {
		t.Fatalf("filed findings after wont-fix = %d, want no new finding", got)
	}

	role := janitorRoleBead(ctx, t, store)
	events, err := store.Journey(ctx, role.ID, time.Time{})
	if err != nil {
		t.Fatalf("janitor journey: %v", err)
	}
	if !janitorJourneyHasSkipped(events, "deadcode") {
		t.Fatalf("janitor journey = %#v, want skipped deadcode detector", events)
	}
}

func janitorFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module fixture\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dead.go"), []byte("package fixture\n\n// TODO: intentional fixture dead code\nfunc dead() {}\n"), 0o600); err != nil {
		t.Fatalf("write fixture source: %v", err)
	}
	for _, args := range [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.name=Oro Test", "-c", "user.email=oro@example.test", "commit", "-m", "fixture", "--date", "2000-01-01T00:00:00Z"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return repo
}

func janitorFiledFindings(ctx context.Context, t *testing.T, store beadstore.Store) []*protocol.Bead {
	t.Helper()
	beads, err := store.FindByMetadataKey(ctx, janitorFindingMetadataKey)
	if err != nil {
		t.Fatalf("find filed janitor findings: %v", err)
	}
	return beads
}

func janitorRoleBead(ctx context.Context, t *testing.T, store beadstore.Store) *protocol.Bead {
	t.Helper()
	beads, err := store.FindByMetadataKey(ctx, janitorRoleMetadataKey)
	if err != nil {
		t.Fatalf("find janitor role: %v", err)
	}
	if len(beads) != 1 {
		t.Fatalf("janitor role beads = %d, want 1", len(beads))
	}
	return beads[0]
}

func janitorJourneyHasSkipped(events []beadstore.JourneyEvent, detector string) bool {
	for _, event := range events {
		if event.Event == "janitor_cycle" && strings.Contains(event.Payload, `"`+detector+`"`) {
			return true
		}
	}
	return false
}
