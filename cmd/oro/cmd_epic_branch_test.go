package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/dispatcher"
)

type stubEpicBranchInspector struct {
	err       error
	calls     int
	branch    string
	target    string
	onInspect func()
}

func (s *stubEpicBranchInspector) InspectEpicBranch(_ context.Context, branch, targetBranch string) error {
	s.calls++
	s.branch = branch
	s.target = targetBranch
	if s.onInspect != nil {
		s.onInspect()
	}
	return s.err
}

func TestEpicBranchBlockerCLIListsAndSafelyResolves(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"epic-branch"})
	if err != nil {
		t.Fatalf("find epic-branch command: %v", err)
	}
	if cmd.Name() != "epic-branch" {
		t.Fatalf("command = %q, want epic-branch", cmd.Name())
	}

	t.Run("list JSON is complete and branch stable", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		db, err := openStateDB(dbPath)
		if err != nil {
			t.Fatalf("open state db: %v", err)
		}
		for _, row := range []struct {
			branch, epicID, state string
		}{
			{branch: "epic/oro-z", epicID: "oro-z", state: "blocked"},
			{branch: "epic/oro-a", epicID: "oro-a", state: "blocked"},
			{branch: "epic/oro-resolved", epicID: "oro-resolved", state: "resolved"},
		} {
			if _, err := db.Exec(`
INSERT INTO epic_branch_admissions (
    branch, epic_id, target_branch, state, generation,
    lease_token, lease_owner, lease_expires_at, blocker_kind, checkout_path,
    branch_sha, target_sha, recovery_bead_id, details, created_at, updated_at, resolved_at
) VALUES (?, ?, 'main', ?, 4, 'token', 'worker-1', '2026-08-03T10:00:00Z',
          'diverged', '/tmp/epic', 'branch-sha', 'target-sha', 'oro-recovery',
          'fresh evidence required', '2026-08-03T09:00:00Z', '2026-08-03T09:30:00Z',
          CASE WHEN ? = 'resolved' THEN '2026-08-03T09:45:00Z' END)`,
				row.branch, row.epicID, row.state, row.state); err != nil {
				t.Fatalf("seed admission %s: %v", row.branch, err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close seeded db: %v", err)
		}

		t.Setenv("ORO_DB_PATH", dbPath)
		root := newRootCmd()
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetArgs([]string{"epic-branch", "list", "--json"})
		if err := root.Execute(); err != nil {
			t.Fatalf("execute epic-branch list: %v", err)
		}
		var records []map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
			t.Fatalf("decode list JSON %q: %v", stdout.String(), err)
		}
		if len(records) != 2 {
			t.Fatalf("blocked records = %d, want 2: %+v", len(records), records)
		}
		if got := []string{records[0]["branch"].(string), records[1]["branch"].(string)}; got[0] != "epic/oro-a" || got[1] != "epic/oro-z" {
			t.Fatalf("branch order = %v, want [epic/oro-a epic/oro-z]", got)
		}
		for _, field := range []string{
			"branch", "epic_id", "target_branch", "state", "generation", "lease_owner",
			"lease_expires_at", "blocker_kind", "checkout_path", "branch_sha", "target_sha",
			"recovery_bead_id", "details", "resolved_at",
		} {
			if _, ok := records[0][field]; !ok {
				t.Errorf("list JSON omits %q: %+v", field, records[0])
			}
		}
	})

	t.Run("resolve requires fresh safe inspection and changes only its row", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		db, err := openStateDB(dbPath)
		if err != nil {
			t.Fatalf("open state db: %v", err)
		}
		seedBlockedEpicBranchAdmission(t, db, "epic/oro-e", "oro-e", 4)
		seedBlockedEpicBranchAdmission(t, db, "epic/oro-other", "oro-other", 7)
		if _, err := db.Exec(`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-work', 'worker-1', '/tmp/work', 'active')`); err != nil {
			t.Fatalf("seed assignment: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO recovery_quarantines (bead_id, reason, details) VALUES ('oro-work', 'manual', 'preserve')`); err != nil {
			t.Fatalf("seed recovery quarantine: %v", err)
		}
		for _, fixture := range []struct {
			description string
			statement   string
		}{
			{description: "create runtime lease", statement: `CREATE TABLE runtime_leases (id TEXT PRIMARY KEY, marker TEXT NOT NULL)`},
			{description: "seed runtime lease", statement: `INSERT INTO runtime_leases (id, marker) VALUES ('lease-1', 'unchanged')`},
			{description: "create storage lease", statement: `CREATE TABLE leases (id TEXT PRIMARY KEY, marker TEXT NOT NULL)`},
			{description: "seed storage lease", statement: `INSERT INTO leases (id, marker) VALUES ('lease-1', 'unchanged')`},
		} {
			if _, err := db.Exec(fixture.statement); err != nil {
				t.Fatalf("%s: %v", fixture.description, err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close seeded db: %v", err)
		}

		inspector := &stubEpicBranchInspector{}
		t.Setenv("ORO_DB_PATH", dbPath)
		cmd := newEpicBranchCmdWithInspector(inspector)
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetArgs([]string{"resolve", "epic/oro-e", "--generation", "4"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("resolve epic branch: %v", err)
		}
		if inspector.calls != 1 || inspector.branch != "epic/oro-e" || inspector.target != "main" {
			t.Fatalf("inspection = calls:%d branch:%q target:%q", inspector.calls, inspector.branch, inspector.target)
		}
		if !strings.Contains(stdout.String(), "generation 4") {
			t.Fatalf("resolve output %q does not contain persisted generation", stdout.String())
		}

		db, err = openDB(dbPath)
		if err != nil {
			t.Fatalf("reopen state db: %v", err)
		}
		defer db.Close()
		assertAdmissionState(t, db, "epic/oro-e", "resolved", 4, true)
		assertAdmissionState(t, db, "epic/oro-other", "blocked", 7, false)
		assertScalar(t, db, `SELECT status FROM assignments WHERE bead_id = 'oro-work'`, "active")
		assertScalar(t, db, `SELECT status FROM recovery_quarantines WHERE bead_id = 'oro-work'`, "open")
		assertScalar(t, db, `SELECT marker FROM runtime_leases WHERE id = 'lease-1'`, "unchanged")
		assertScalar(t, db, `SELECT marker FROM leases WHERE id = 'lease-1'`, "unchanged")
	})

	t.Run("omitted generation fails without inspection or mutation", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		db, err := openStateDB(dbPath)
		if err != nil {
			t.Fatalf("open state db: %v", err)
		}
		seedBlockedEpicBranchAdmission(t, db, "epic/oro-e", "oro-e", 4)
		if err := db.Close(); err != nil {
			t.Fatalf("close seeded db: %v", err)
		}

		inspector := &stubEpicBranchInspector{}
		t.Setenv("ORO_DB_PATH", dbPath)
		cmd := newEpicBranchCmdWithInspector(inspector)
		cmd.SetArgs([]string{"resolve", "epic/oro-e"})
		if err := cmd.Execute(); err == nil {
			t.Fatal("resolve without generation succeeded")
		}
		if inspector.calls != 0 {
			t.Fatalf("inspection calls = %d, want 0", inspector.calls)
		}
		db, err = openDB(dbPath)
		if err != nil {
			t.Fatalf("reopen state db: %v", err)
		}
		defer db.Close()
		assertAdmissionState(t, db, "epic/oro-e", "blocked", 4, false)
	})

	for _, tc := range []struct {
		name       string
		generation int64
		inspectErr error
		wantCalls  int
	}{
		{name: "stale generation", generation: 3, wantCalls: 0},
		{name: "still checked out", generation: 4, inspectErr: errors.New("branch is checked out"), wantCalls: 1},
		{name: "diverged", generation: 4, inspectErr: errors.New("branch diverged"), wantCalls: 1},
		{name: "missing", generation: 4, inspectErr: errors.New("branch missing"), wantCalls: 1},
		{name: "inspection error", generation: 4, inspectErr: errors.New("git unavailable"), wantCalls: 1},
	} {
		t.Run(tc.name+" remains blocked", func(t *testing.T) {
			db, err := openStateDB(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("open state db: %v", err)
			}
			defer db.Close()
			seedBlockedEpicBranchAdmission(t, db, "epic/oro-e", "oro-e", 4)
			inspector := &stubEpicBranchInspector{err: tc.inspectErr}
			if _, err := resolveEpicBranchAdmission(context.Background(), db, inspector, "epic/oro-e", tc.generation); err == nil {
				t.Fatal("unsafe resolve succeeded")
			}
			if inspector.calls != tc.wantCalls {
				t.Fatalf("inspection calls = %d, want %d", inspector.calls, tc.wantCalls)
			}
			assertAdmissionState(t, db, "epic/oro-e", "blocked", 4, false)
		})
	}

	t.Run("generation race after inspection fails closed", func(t *testing.T) {
		db, err := openStateDB(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("open state db: %v", err)
		}
		defer db.Close()
		seedBlockedEpicBranchAdmission(t, db, "epic/oro-e", "oro-e", 4)
		inspector := &stubEpicBranchInspector{onInspect: func() {
			if _, updateErr := db.Exec(`UPDATE epic_branch_admissions SET generation = 5 WHERE branch = 'epic/oro-e'`); updateErr != nil {
				t.Fatalf("advance generation during inspection: %v", updateErr)
			}
		}}
		if _, err := resolveEpicBranchAdmission(context.Background(), db, inspector, "epic/oro-e", 4); err == nil {
			t.Fatal("resolve succeeded after generation changed during inspection")
		}
		assertAdmissionState(t, db, "epic/oro-e", "blocked", 5, false)
	})

	t.Run("production inspector accepts only present unchecked non-diverged refs", func(t *testing.T) {
		repo := t.TempDir()
		runRecoveryGit(t, repo, "init", "-b", "main")
		runRecoveryGit(t, repo, "config", "user.email", "test@example.com")
		runRecoveryGit(t, repo, "config", "user.name", "Oro Test")
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("root\n"), 0o600); err != nil {
			t.Fatalf("write root: %v", err)
		}
		runRecoveryGit(t, repo, "add", "tracked.txt")
		runRecoveryGit(t, repo, "commit", "-m", "root")
		runRecoveryGit(t, repo, "branch", "epic/safe")

		inspector := dispatcher.NewGitWorktreeManager(repo, "", "", &dispatcher.ExecCommandRunner{})
		if err := inspector.InspectEpicBranch(context.Background(), "epic/safe", "main"); err != nil {
			t.Fatalf("inspect safe branch: %v", err)
		}
		if err := inspector.InspectEpicBranch(context.Background(), "epic/missing", "main"); err == nil {
			t.Fatal("missing branch inspected as safe")
		}

		runRecoveryGit(t, repo, "checkout", "epic/safe")
		if err := inspector.InspectEpicBranch(context.Background(), "epic/safe", "main"); err == nil {
			t.Fatal("checked-out branch inspected as safe")
		}
		runRecoveryGit(t, repo, "checkout", "main")

		runRecoveryGit(t, repo, "checkout", "-b", "epic/diverged")
		if err := os.WriteFile(filepath.Join(repo, "epic.txt"), []byte("epic\n"), 0o600); err != nil {
			t.Fatalf("write epic: %v", err)
		}
		runRecoveryGit(t, repo, "add", "epic.txt")
		runRecoveryGit(t, repo, "commit", "-m", "epic")
		runRecoveryGit(t, repo, "checkout", "main")
		if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o600); err != nil {
			t.Fatalf("write main: %v", err)
		}
		runRecoveryGit(t, repo, "add", "main.txt")
		runRecoveryGit(t, repo, "commit", "-m", "main")
		if err := inspector.InspectEpicBranch(context.Background(), "epic/diverged", "main"); err == nil {
			t.Fatal("diverged branch inspected as safe")
		}
	})
}

func seedBlockedEpicBranchAdmission(t *testing.T, db *sql.DB, branch, epicID string, generation int64) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO epic_branch_admissions (
    branch, epic_id, target_branch, state, generation, lease_token, lease_owner,
    lease_expires_at, blocker_kind, checkout_path, branch_sha, target_sha,
    recovery_bead_id, details, created_at, updated_at
) VALUES (?, ?, 'main', 'blocked', ?, 'token', 'worker-1', '2026-08-03T10:00:00Z',
          'diverged', '/tmp/epic', 'branch-sha', 'target-sha', 'oro-recovery',
          'fresh evidence required', '2026-08-03T09:00:00Z', '2026-08-03T09:30:00Z')`,
		branch, epicID, generation); err != nil {
		t.Fatalf("seed admission %s: %v", branch, err)
	}
}

func assertAdmissionState(t *testing.T, db *sql.DB, branch, wantState string, wantGeneration int64, wantResolved bool) {
	t.Helper()
	var state string
	var generation int64
	var resolvedAt sql.NullString
	if err := db.QueryRow(`SELECT state, generation, resolved_at FROM epic_branch_admissions WHERE branch = ?`, branch).Scan(&state, &generation, &resolvedAt); err != nil {
		t.Fatalf("read admission %s: %v", branch, err)
	}
	if state != wantState || generation != wantGeneration || resolvedAt.Valid != wantResolved {
		t.Fatalf("admission %s = state:%q generation:%d resolved:%v, want state:%q generation:%d resolved:%v", branch, state, generation, resolvedAt.Valid, wantState, wantGeneration, wantResolved)
	}
}

func assertScalar(t *testing.T, db *sql.DB, query, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("query scalar: %v", err)
	}
	if got != want {
		t.Fatalf("scalar = %q, want %q", got, want)
	}
}
