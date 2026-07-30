package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestStorageStatusCommand(t *testing.T) {
	t.Run("catalog pragmas must prove health", func(t *testing.T) {
		if !storageCatalogPragmasHealthy("ok", storage.CatalogSchemaVersion) {
			t.Error("healthy catalog pragmas reported unhealthy")
		}
		if storageCatalogPragmasHealthy("row 7 missing from index", storage.CatalogSchemaVersion) {
			t.Error("integrity diagnostic reported healthy")
		}
		if storageCatalogPragmasHealthy("ok", storage.CatalogSchemaVersion+1) {
			t.Error("unsupported catalog version reported healthy")
		}
	})

	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		t.Fatalf("ResolveStoragePaths() error = %v", err)
	}
	if err := os.MkdirAll(paths.CacheRoot, 0o750); err != nil {
		t.Fatalf("create cache root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.CacheRoot, "sample"), []byte("12345"), 0o600); err != nil {
		t.Fatalf("write cache sample: %v", err)
	}
	catalog, err := openStorageCatalog(context.Background(), oroHome)
	if err != nil {
		t.Fatalf("openStorageCatalog() error = %v", err)
	}
	completedAt := time.Now().UTC().Truncate(time.Second)
	seedStorageCatalog(t, catalog, filepath.Join(oroHome, "scratch"), completedAt)
	t.Cleanup(func() {
		if err := catalog.Close(); err != nil {
			t.Errorf("close catalog: %v", err)
		}
	})

	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"storage", "status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute storage status: %v", err)
	}

	var got struct {
		Bytes struct {
			Catalog  int64 `json:"catalog"`
			Evidence int64 `json:"evidence"`
			Cache    int64 `json:"cache"`
			Total    int64 `json:"total"`
		} `json:"bytes"`
		Pressure string `json:"pressure"`
		Catalog  struct {
			Health string `json:"health"`
		} `json:"catalog"`
		Leases struct {
			Active int `json:"active"`
		} `json:"leases"`
		Backlog struct {
			PendingSweeps int `json:"pending_sweeps"`
		} `json:"backlog"`
		NextSweep string `json:"next_sweep"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("decode JSON %q: %v", out.String(), err)
	}
	wantCatalogBytes := storageCatalogFilesBytes(t, paths.CatalogPath)
	if got.Bytes.Catalog != wantCatalogBytes || got.Bytes.Evidence != 0 || got.Bytes.Cache != 5 || got.Bytes.Total != wantCatalogBytes+5 {
		t.Errorf("bytes = %+v, want catalog=%d evidence=0 cache=5 total=%d", got.Bytes, wantCatalogBytes, wantCatalogBytes+5)
	}
	if got.Pressure == "" {
		t.Error("pressure is empty")
	}
	if got.Catalog.Health != "healthy" {
		t.Errorf("catalog health = %q, want healthy", got.Catalog.Health)
	}
	if got.Leases.Active != 1 {
		t.Errorf("active leases = %d, want 1", got.Leases.Active)
	}
	if got.Backlog.PendingSweeps != 1 {
		t.Errorf("pending sweeps = %d, want 1", got.Backlog.PendingSweeps)
	}
	wantNextSweep := completedAt.Add(weeklyStorageSweepInterval).Format(time.RFC3339)
	if got.NextSweep != wantNextSweep {
		t.Errorf("next_sweep = %q, want %q", got.NextSweep, wantNextSweep)
	}

	root = newRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetArgs([]string{"storage", "status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute human storage status: %v", err)
	}
	if !strings.Contains(out.String(), "catalog: healthy") {
		t.Errorf("human output missing catalog health:\n%s", out.String())
	}

	t.Run("missing catalog reports preservation mode", func(t *testing.T) {
		missingHome := t.TempDir()
		status, err := loadStorageStatus(context.Background(), missingHome)
		if err != nil {
			t.Fatalf("loadStorageStatus() error = %v", err)
		}
		if status.Catalog.Health != "preservation_mode" {
			t.Errorf("catalog health = %q, want preservation_mode", status.Catalog.Health)
		}
	})

	t.Run("symlinked managed root reports target bytes", func(t *testing.T) {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "payload"), []byte("1234567"), 0o600); err != nil {
			t.Fatalf("write target payload: %v", err)
		}
		link := filepath.Join(t.TempDir(), "cache")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("create cache symlink: %v", err)
		}
		got, err := storagePathBytes(link)
		if err != nil {
			t.Fatalf("storagePathBytes() error = %v", err)
		}
		if got != 7 {
			t.Errorf("storagePathBytes() = %d, want 7", got)
		}
	})

	t.Run("corrupt catalog stays preserved", func(t *testing.T) {
		corruptHome := t.TempDir()
		corruptPaths, err := ResolveStoragePaths(corruptHome)
		if err != nil {
			t.Fatalf("ResolveStoragePaths() error = %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(corruptPaths.CatalogPath), 0o750); err != nil {
			t.Fatalf("create catalog dir: %v", err)
		}
		const corruptContents = "not a sqlite database"
		if err := os.WriteFile(corruptPaths.CatalogPath, []byte(corruptContents), 0o600); err != nil {
			t.Fatalf("write corrupt catalog: %v", err)
		}
		status, err := loadStorageStatus(context.Background(), corruptHome)
		if err != nil {
			t.Fatalf("loadStorageStatus() error = %v", err)
		}
		if status.Catalog.Health != "corrupt" {
			t.Errorf("catalog health = %q, want corrupt", status.Catalog.Health)
		}
		contents, err := os.ReadFile(corruptPaths.CatalogPath)
		if err != nil {
			t.Fatalf("read corrupt catalog: %v", err)
		}
		if string(contents) != corruptContents {
			t.Errorf("corrupt catalog changed to %q", contents)
		}
	})

	t.Run("incomplete catalog schema is unhealthy", func(t *testing.T) {
		incompleteHome := t.TempDir()
		catalog, err := openStorageCatalog(context.Background(), incompleteHome)
		if err != nil {
			t.Fatalf("openStorageCatalog() error = %v", err)
		}
		if _, err := catalog.DB().Exec(`DROP TABLE runtime_tombstones`); err != nil {
			t.Fatalf("remove required runtime table: %v", err)
		}
		if err := catalog.Close(); err != nil {
			t.Fatalf("close incomplete catalog: %v", err)
		}
		status, err := loadStorageStatus(context.Background(), incompleteHome)
		if err != nil {
			t.Fatalf("loadStorageStatus() error = %v", err)
		}
		if status.Catalog.Health != "corrupt" {
			t.Errorf("catalog health = %q, want corrupt", status.Catalog.Health)
		}
	})

	t.Run("catalog foreign key violations are unhealthy", func(t *testing.T) {
		invalidHome := t.TempDir()
		catalog, err := openStorageCatalog(context.Background(), invalidHome)
		if err != nil {
			t.Fatalf("openStorageCatalog() error = %v", err)
		}
		if _, err := catalog.DB().Exec(`
			PRAGMA foreign_keys = OFF;
			INSERT INTO leases (id, namespace_id, owner_id, expires_at, created_at)
			VALUES ('orphan', 'missing', 'worker-1', '2099-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		`); err != nil {
			t.Fatalf("insert invalid catalog lease: %v", err)
		}
		if err := catalog.Close(); err != nil {
			t.Fatalf("close invalid catalog: %v", err)
		}
		status, err := loadStorageStatus(context.Background(), invalidHome)
		if err != nil {
			t.Fatalf("loadStorageStatus() error = %v", err)
		}
		if status.Catalog.Health != "corrupt" {
			t.Errorf("catalog health = %q, want corrupt", status.Catalog.Health)
		}
	})
}

func TestDevCleanupHealthProjection(t *testing.T) {
	ctx := context.Background()
	oroHome := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	catalog, err := openStorageCatalog(ctx, oroHome)
	if err != nil {
		t.Fatalf("openStorageCatalog() error = %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	due := now.Add(-25 * time.Hour)
	lastSuccess := now.Add(-8 * 24 * time.Hour)
	lastAttempt := now.Add(-time.Hour)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO providers (id, created_at, updated_at) VALUES ('go', ?, ?)`, []any{lastSuccess.Format(time.RFC3339), lastSuccess.Format(time.RFC3339)}},
		{`INSERT INTO providers (id, created_at, updated_at) VALUES ('uv', ?, ?)`, []any{lastAttempt.Format(time.RFC3339), lastAttempt.Format(time.RFC3339)}},
		{`INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at) VALUES ('weekly-dev-cache', ?, ?)`, []any{due.Format(time.RFC3339), due.Format(time.RFC3339)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, finished_at, status) VALUES ('weekly-go', 'go', ?, ?, 'completed')`, []any{lastSuccess.Format(time.RFC3339), lastSuccess.Format(time.RFC3339)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, finished_at, status) VALUES ('weekly-uv', 'uv', ?, ?, 'failed')`, []any{lastAttempt.Format(time.RFC3339), lastAttempt.Format(time.RFC3339)}},
		{`INSERT INTO evidence (id, sweep_id, kind, payload, created_at) VALUES ('weekly-go-evidence', 'weekly-go', 'weekly_dev_cache_provider', ?, ?)`, []any{`{"provider_id":"go","before":{"used_bytes":400},"after":{"used_bytes":125},"exit_code":0}`, lastSuccess.Format(time.RFC3339)}},
		{`INSERT INTO evidence (id, sweep_id, kind, payload, created_at) VALUES ('weekly-uv-evidence', 'weekly-uv', 'weekly_dev_cache_provider', ?, ?)`, []any{`{"provider_id":"uv","before":{"used_bytes":800},"after":{"used_bytes":800},"exit_code":19}`, lastAttempt.Format(time.RFC3339)}},
		{`INSERT INTO runtime_pause_epochs (epoch, state, created_at) VALUES (7, 'paused', ?)`, []any{lastAttempt.Format(time.RFC3339)}},
		{`INSERT INTO runtime_pause_acknowledgements (epoch, controller_id, state, acknowledged_at) VALUES (7, 'dispatcher', 'paused', ?)`, []any{lastAttempt.Format(time.RFC3339)}},
	} {
		if _, err := catalog.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}

	status, err := loadStorageStatus(ctx, oroHome)
	if err != nil {
		t.Fatalf("loadStorageStatus() error = %v", err)
	}
	if status.DevCleanup.LastAttempt != lastAttempt.Format(time.RFC3339) || status.DevCleanup.LastSuccess != lastSuccess.Format(time.RFC3339) {
		t.Fatalf("cleanup attempts = %+v, want attempt=%s success=%s", status.DevCleanup, lastAttempt, lastSuccess)
	}
	if status.DevCleanup.NextDue != due.Format(time.RFC3339) || status.DevCleanup.OverdueBySeconds < int64((24*time.Hour).Seconds()) {
		t.Fatalf("cleanup schedule = %+v, want next_due=%s and overdue by >=24h", status.DevCleanup, due)
	}
	if status.DevCleanup.FreedBytes != 275 || len(status.DevCleanup.Providers) != 2 {
		t.Fatalf("cleanup provider projection = %+v, want 275 freed bytes and two providers", status.DevCleanup)
	}
	if status.DevCleanup.Providers[0].ProviderID != "go" || status.DevCleanup.Providers[0].Status != "completed" || status.DevCleanup.Providers[1].ProviderID != "uv" || status.DevCleanup.Providers[1].Status != "failed" {
		t.Fatalf("cleanup provider results = %+v, want completed go and failed uv", status.DevCleanup.Providers)
	}
	if status.DevCleanup.Pause.State != storage.Paused || !status.DevCleanup.Pause.Drained {
		t.Fatalf("cleanup pause = %+v, want paused and drained", status.DevCleanup.Pause)
	}

	health := loadFactoryStorageHealth(ctx, oroHome)
	if !health.SweepOverdue || !health.AdmissionPaused || health.DevCleanup == nil {
		t.Fatalf("factory storage health = %+v, want overdue paused cleanup health", health)
	}
	if health.DevCleanup.FreedBytes != status.DevCleanup.FreedBytes || health.DevCleanup.LastAttempt != status.DevCleanup.LastAttempt || health.DevCleanup.Pause != status.DevCleanup.Pause || len(health.DevCleanup.Providers) != len(status.DevCleanup.Providers) {
		t.Fatalf("status and health cleanup projections differ: status=%+v health=%+v", status.DevCleanup, health.DevCleanup)
	}
}

func TestStorageCleanDefaultsToDryRun(t *testing.T) {
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	target := filepath.Join(t.TempDir(), "stale-runtime")
	if err := os.WriteFile(target, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write cleanup target: %v", err)
	}
	leasedTarget := filepath.Join(t.TempDir(), "leased-runtime")
	if err := os.WriteFile(leasedTarget, []byte("leased"), 0o600); err != nil {
		t.Fatalf("write leased cleanup target: %v", err)
	}
	catalog, err := openStorageCatalog(context.Background(), oroHome)
	if err != nil {
		t.Fatalf("openStorageCatalog() error = %v", err)
	}
	catalogClosed := false
	t.Cleanup(func() {
		if catalogClosed {
			return
		}
		if err := catalog.Close(); err != nil {
			t.Errorf("close catalog: %v", err)
		}
	})
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO providers (id, created_at, updated_at) VALUES ('runtime', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO namespaces (id, provider_id, path, created_at, updated_at) VALUES ('stale-runtime', 'runtime', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, []any{target}},
		{`INSERT INTO namespaces (id, provider_id, path, created_at, updated_at) VALUES ('leased-runtime', 'runtime', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, []any{leasedTarget}},
		{`INSERT INTO leases (id, namespace_id, owner_id, expires_at, created_at) VALUES ('lease-1', 'leased-runtime', 'worker-1', '2099-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, nil},
	} {
		if _, err := catalog.DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed cleanup catalog: %v", err)
		}
	}

	var out strings.Builder
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"storage", "clean", "--scope", "runtime", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute dry-run clean: %v", err)
	}
	var dryRun struct {
		Scope     storage.Scope `json:"scope"`
		Apply     bool          `json:"apply"`
		Decisions []struct {
			Path           string                 `json:"path"`
			Action         storage.ActionType     `json:"action"`
			PreserveReason storage.PreserveReason `json:"preserve_reason"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out.String()), &dryRun); err != nil {
		t.Fatalf("decode dry-run JSON %q: %v", out.String(), err)
	}
	if dryRun.Scope != storage.ScopeRuntime || dryRun.Apply {
		t.Errorf("dry-run scope/apply = (%q, %t), want (runtime, false)", dryRun.Scope, dryRun.Apply)
	}
	dryRunByPath := make(map[string]struct {
		action storage.ActionType
		reason storage.PreserveReason
	}, len(dryRun.Decisions))
	for _, decision := range dryRun.Decisions {
		dryRunByPath[decision.Path] = struct {
			action storage.ActionType
			reason storage.PreserveReason
		}{decision.Action, decision.PreserveReason}
	}
	if len(dryRun.Decisions) != 2 || dryRunByPath[target].action != storage.Preserve || dryRunByPath[target].reason != storage.PreserveNoAuthority || dryRunByPath[leasedTarget].action != storage.Preserve || dryRunByPath[leasedTarget].reason != storage.PreserveActive {
		t.Errorf("dry-run decisions = %+v, want preserved runtime candidate without authority", dryRun.Decisions)
	}
	for _, path := range []string{target, leasedTarget} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry-run mutated cleanup target %q: %v", path, err)
		}
	}
	root = newRootCmd()
	root.SetArgs([]string{"storage", "clean", "--scope", "runtime", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute explicit dry-run clean: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("explicit dry-run mutated cleanup target: %v", err)
	}

	out.Reset()
	root = newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"storage", "clean", "--scope", "runtime", "--apply", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute apply clean: %v", err)
	}
	var applied struct {
		Decisions []struct {
			Path           string                 `json:"path"`
			Action         storage.ActionType     `json:"action"`
			PreserveReason storage.PreserveReason `json:"preserve_reason"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out.String()), &applied); err != nil {
		t.Fatalf("decode apply JSON %q: %v", out.String(), err)
	}
	appliedByPath := make(map[string]struct {
		action storage.ActionType
		reason storage.PreserveReason
	}, len(applied.Decisions))
	for _, decision := range applied.Decisions {
		appliedByPath[decision.Path] = struct {
			action storage.ActionType
			reason storage.PreserveReason
		}{decision.Action, decision.PreserveReason}
	}
	if len(applied.Decisions) != 2 || appliedByPath[target].action != storage.Delete || appliedByPath[leasedTarget].action != storage.Preserve || appliedByPath[leasedTarget].reason != storage.PreserveActive {
		t.Errorf("apply decisions = %+v, want delete plus active preservation", applied.Decisions)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("apply did not remove proven target: %v", err)
	}
	if _, err := os.Stat(leasedTarget); err != nil {
		t.Errorf("--apply bypassed active lease proof: %v", err)
	}

	if err := catalog.Close(); err != nil {
		t.Fatalf("close catalog before corruption: %v", err)
	}
	catalogClosed = true
	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		t.Fatalf("ResolveStoragePaths() error = %v", err)
	}
	if err := os.WriteFile(paths.CatalogPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("corrupt cleanup catalog: %v", err)
	}
	out.Reset()
	root = newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"storage", "clean", "--scope", "runtime", "--apply", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute corrupt apply clean: %v", err)
	}
	var corrupt struct {
		CatalogHealthy bool `json:"catalog_healthy"`
		Decisions      []struct {
			Action storage.ActionType `json:"action"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out.String()), &corrupt); err != nil {
		t.Fatalf("decode corrupt cleanup JSON %q: %v", out.String(), err)
	}
	if corrupt.CatalogHealthy || len(corrupt.Decisions) != 0 {
		t.Errorf("corrupt catalog cleanup = %+v, want preservation-only empty plan", corrupt)
	}
	if _, err := os.Stat(leasedTarget); err != nil {
		t.Errorf("corrupt catalog apply mutated preserved target: %v", err)
	}

	root = newRootCmd()
	root.SetArgs([]string{"storage", "clean", "--scope", "outside"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "invalid storage cleanup scope") {
		t.Errorf("unknown scope error = %v, want usage error", err)
	}
}

func TestStorageCleanOroHomeUsesAllowlistedEvidencePlan(t *testing.T) {
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	logPath := filepath.Join(oroHome, "logs", "expired.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("expired"), 0o600); err != nil {
		t.Fatalf("write expired log: %v", err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatalf("age expired log: %v", err)
	}

	var out strings.Builder
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"storage", "clean", "--scope", "oro-home", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute Oro-home dry-run clean: %v", err)
	}

	var result struct {
		Scope     storage.Scope `json:"scope"`
		Apply     bool          `json:"apply"`
		Decisions []struct {
			Path        string                 `json:"path"`
			Scope       storage.Scope          `json:"scope"`
			Reason      storage.RetentionClass `json:"reason"`
			BeforeBytes int64                  `json:"before_bytes"`
			AfterBytes  int64                  `json:"after_bytes"`
			Changed     bool                   `json:"changed"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatalf("decode Oro-home cleanup JSON %q: %v", out.String(), err)
	}
	if result.Scope != storage.ScopeOroHome || result.Apply {
		t.Errorf("scope/apply = (%q, %t), want (oro-home, false)", result.Scope, result.Apply)
	}
	if len(result.Decisions) != 1 {
		t.Fatalf("Oro-home decisions = %+v, want one allowlisted log", result.Decisions)
	}
	decision := result.Decisions[0]
	if decision.Path != "logs/expired.log" || decision.Scope != storage.ScopeOroHome || decision.Reason != storage.RetentionLog || decision.BeforeBytes != 7 || decision.AfterBytes != 7 || decision.Changed {
		t.Errorf("Oro-home decision = %+v, want unchanged log evidence", decision)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("dry-run removed allowlisted log: %v", err)
	}
}

func storageCatalogFilesBytes(t *testing.T, catalogPath string) int64 {
	t.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(catalogPath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat catalog file %q: %v", catalogPath+suffix, err)
		}
		total += info.Size()
	}
	return total
}

func seedStorageCatalog(t *testing.T, catalog *storage.Catalog, scratchPath string, completedAt time.Time) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO providers (id, created_at, updated_at) VALUES ('runtime', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO namespaces (id, provider_id, path, created_at, updated_at) VALUES ('scratch', 'runtime', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, []any{scratchPath}},
		{`INSERT INTO leases (id, namespace_id, owner_id, expires_at, created_at) VALUES ('lease-1', 'scratch', 'worker-1', ?, '2026-01-01T00:00:00Z')`, []any{time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, finished_at, status) VALUES ('weekly-complete', 'runtime', '2026-01-01T00:00:00Z', ?, 'completed')`, []any{completedAt.Format(time.RFC3339)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, finished_at, status) VALUES ('weekly-failed', 'runtime', '2026-01-02T00:00:00Z', ?, 'failed')`, []any{completedAt.Add(24 * time.Hour).Format(time.RFC3339)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, status) VALUES ('pending', 'runtime', '2026-01-02T00:00:00Z', 'running')`, nil},
	}
	for _, statement := range statements {
		if _, err := catalog.DB().ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}
}
