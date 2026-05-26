package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestMemoryRetirementReadinessZeroWaitAllowsRecentTelemetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)

	t.Run("recent telemetry and clean scan is ready", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		insertMemoryReadEvent(t, db, now.Add(-time.Hour))
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":     "module example.com/fixture\n\ngo 1.23\n",
			"pkg/app.go": "package pkg\n",
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if !report.Ready {
			t.Fatalf("recent telemetry with a clean import scan should be ready, blockers=%v", report.Blockers)
		}
		if retirementBlockersContain(report, "recent memory reads") {
			t.Fatalf("recent telemetry should not be a blocker with zero wait, blockers=%v", report.Blockers)
		}
	})

	t.Run("missing telemetry still fails closed", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":     "module example.com/fixture\n\ngo 1.23\n",
			"pkg/app.go": "package pkg\n",
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if report.Ready {
			t.Fatal("missing telemetry should fail closed")
		}
		if !retirementBlockersContain(report, "no memory read telemetry") {
			t.Fatalf("blockers = %v, want missing telemetry blocker", report.Blockers)
		}
	})

	t.Run("live imports still fail closed", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		insertMemoryReadEvent(t, db, now.Add(-time.Hour))
		root := writeRetirementFixture(t, map[string]string{
			"go.mod": "module example.com/fixture\n\ngo 1.23\n",
			"pkg/live/live.go": `package live

import "oro/pkg/memory"

var _ = memory.Store{}
`,
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if report.Ready {
			t.Fatal("live production imports should fail closed")
		}
		if !retirementBlockersContain(report, "live pkg/memory imports") {
			t.Fatalf("blockers = %v, want live import blocker", report.Blockers)
		}
	})
}

func TestMemoryRetirementReadinessGate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	t.Run("nil DB reports not ready", func(t *testing.T) {
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":     "module example.com/fixture\n\ngo 1.23\n",
			"pkg/app.go": "package pkg\n",
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, nil, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if report.Ready {
			t.Fatal("nil DB must fail closed")
		}
		if !retirementBlockersContain(report, "telemetry database unavailable") {
			t.Fatalf("blockers = %v, want nil DB telemetry blocker", report.Blockers)
		}
	})

	t.Run("empty scan root errors", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		_, err := EvaluateMemoryRetirementReadiness(ctx, db, now, "")
		if err == nil {
			t.Fatal("empty scan root must error")
		}
	})

	t.Run("missing read telemetry fails closed inside retirement window", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":     "module example.com/fixture\n\ngo 1.23\n",
			"pkg/app.go": "package pkg\n",
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if report.Ready {
			t.Fatal("missing memory_read_events rows must fail closed")
		}
		if !retirementBlockersContain(report, "no memory read telemetry") {
			t.Fatalf("blockers = %v, want missing telemetry blocker", report.Blockers)
		}
	})

	t.Run("recent read telemetry satisfies telemetry requirement", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		insertMemoryReadEvent(t, db, now.Add(-time.Hour))
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":     "module example.com/fixture\n\ngo 1.23\n",
			"pkg/app.go": "package pkg\n",
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if !report.Ready {
			t.Fatalf("recent read telemetry with clean imports should be ready, blockers=%v", report.Blockers)
		}
		if retirementBlockersContain(report, "recent memory reads") {
			t.Fatalf("recent reads should not block with zero wait, got %v", report.Blockers)
		}
	})

	t.Run("live production pkg memory imports fail except allowlisted files", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		insertMemoryReadEvent(t, db, now.Add(-15*24*time.Hour))
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":                                 "module example.com/fixture\n\ngo 1.23\n",
			"pkg/live/live.go":                       "package live\n\nimport \"oro/pkg/memory\"\n",
			"cmd/oro/cmd_cards_check_drift.go":       "package main\n\nimport \"oro/pkg/memory\"\n",
			"pkg/cards/legacy_writer.go":             "package cards\n\nimport \"oro/pkg/memory\"\n",
			"internal/memoryboundary/store.go":       "package memoryboundary\n\nimport \"oro/pkg/memory\"\n",
			"pkg/live/live_test.go":                  "package live\n\nimport \"oro/pkg/memory\"\n",
			"vendor/example.com/dep/dep.go":          "package dep\n\nimport \"oro/pkg/memory\"\n",
			".git/ignored.go":                        "package git\n\nimport \"oro/pkg/memory\"\n",
			".worktrees/agent/pkg/ignored.go":        "package ignored\n\nimport \"oro/pkg/memory\"\n",
			"ad_hoc/memory_eval/harness.go":          "package memoryeval\n\nimport \"oro/pkg/memory\"\n",
			"pkg/migrate/migrate_memory.go":          "package migrate\n\nimport \"oro/pkg/memory\"\n",
			"pkg/migrations/memory_migration.go":     "package migrations\n\nimport \"oro/pkg/memory\"\n",
			"pkg/migration/memory_migration_file.go": "package migration\n\nimport \"oro/pkg/memory\"\n",
		})

		report, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err != nil {
			t.Fatalf("EvaluateMemoryRetirementReadiness: %v", err)
		}
		if report.Ready {
			t.Fatal("live production import must fail readiness")
		}
		if !retirementBlockersContain(report, "live pkg/memory imports") {
			t.Fatalf("blockers = %v, want live import blocker", report.Blockers)
		}
		if got, want := len(report.LiveImports), 4; got != want {
			t.Fatalf("LiveImports count = %d, want %d (%v)", got, want, report.LiveImports)
		}
		for _, live := range report.LiveImports {
			if strings.HasSuffix(live.Path, "_test.go") ||
				strings.Contains(live.Path, "vendor/") ||
				strings.Contains(live.Path, ".git/") ||
				strings.Contains(live.Path, ".worktrees/") ||
				strings.Contains(live.Path, "ad_hoc/memory_eval/") ||
				live.Path == "cmd/oro/cmd_cards_check_drift.go" ||
				live.Path == "pkg/cards/legacy_writer.go" ||
				live.Path == "internal/memoryboundary/store.go" {
				t.Fatalf("ignored or allowlisted import reported live: %+v", live)
			}
		}
	})

	t.Run("ad hoc ignore is top level scan behavior only", func(t *testing.T) {
		for _, name := range []string{"vendor", ".git", ".worktrees"} {
			if !isMemoryRetirementIgnoredDir(name) {
				t.Fatalf("%s must remain an always-ignored directory", name)
			}
		}
		if isMemoryRetirementIgnoredDir("ad_hoc") {
			t.Fatal("ad_hoc requires scan root context and must not be ignored by name alone")
		}
	})

	t.Run("malformed import file returns scan error", func(t *testing.T) {
		db := newRetirementTelemetryDB(t)
		insertMemoryReadEvent(t, db, now.Add(-15*24*time.Hour))
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":        "module example.com/fixture\n\ngo 1.23\n",
			"pkg/broken.go": "package broken\n\nimport \"oro/pkg/memory\"\nfunc",
		})

		_, err := EvaluateMemoryRetirementReadiness(ctx, db, now, root)
		if err == nil {
			t.Fatal("malformed Go import file must return scan error")
		}
		if !strings.Contains(err.Error(), "scan") {
			t.Fatalf("error = %v, want scan error", err)
		}
	})

	t.Run("command exits nonzero until ready and zero for clean fixture", func(t *testing.T) {
		root := writeRetirementFixture(t, map[string]string{
			"go.mod":     "module example.com/fixture\n\ngo 1.23\n",
			"pkg/app.go": "package pkg\n",
		})
		t.Setenv("ORO_HOME", filepath.Join(root, ".oro-home"))
		t.Setenv("ORO_PROJECT", "")
		t.Chdir(root)

		_, _, err := executeCommand("cards", "memory-retirement-check")
		if err == nil {
			t.Fatal("command must exit nonzero until old read telemetry exists")
		}

		paths, err := ResolveDaemonPaths()
		if err != nil {
			t.Fatalf("ResolveDaemonPaths: %v", err)
		}
		db, err := openStateDB(paths.StateDBPath)
		if err != nil {
			t.Fatalf("openStateDB: %v", err)
		}
		insertMemoryReadEvent(t, db, time.Now())
		if err := db.Close(); err != nil {
			t.Fatalf("close state db: %v", err)
		}

		out, _, err := executeCommand("cards", "memory-retirement-check")
		if err != nil {
			t.Fatalf("command should be ready for clean fixture: %v", err)
		}
		if !strings.Contains(out, "ready") {
			t.Fatalf("stdout = %q, want ready", out)
		}
	})
}

func newRetirementTelemetryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("migrate memory_read_events: %v", err)
	}
	return db
}

func insertMemoryReadEvent(t *testing.T, db *sql.DB, ts time.Time) {
	t.Helper()
	_, err := db.ExecContext(
		context.Background(),
		`INSERT INTO memory_read_events (ts, project, operation) VALUES (?, 'test', 'read')`,
		ts.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert memory_read_events: %v", err)
	}
}

func writeRetirementFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func retirementBlockersContain(report MemoryRetirementReadiness, want string) bool {
	for _, blocker := range report.Blockers {
		if strings.Contains(blocker, want) {
			return true
		}
	}
	return false
}
