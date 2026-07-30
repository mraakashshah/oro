package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/factoryhealth"
	"oro/pkg/storage"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"
)

const (
	weeklyStorageSweepInterval = 7 * 24 * time.Hour
)

type storageStatus struct {
	CapacityBytes uint64 `json:"capacity_bytes"`
	FreeBytes     uint64 `json:"free_bytes"`
	Pressure      string `json:"pressure"`
	Bytes         struct {
		Catalog  int64 `json:"catalog"`
		Evidence int64 `json:"evidence"`
		Cache    int64 `json:"cache"`
		Total    int64 `json:"total"`
	} `json:"bytes"`
	Catalog struct {
		Health string `json:"health"`
	} `json:"catalog"`
	Leases struct {
		Active int `json:"active"`
	} `json:"leases"`
	Backlog struct {
		PendingSweeps int `json:"pending_sweeps"`
	} `json:"backlog"`
	LastSweep  string                   `json:"last_sweep,omitempty"`
	NextSweep  string                   `json:"next_sweep,omitempty"`
	DevCleanup storage.DevCleanupHealth `json:"dev_cleanup"`
}

// newStorageCmd creates the storage inspection and cleanup command group.
func newStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Inspect Oro-managed storage",
	}
	cmd.AddCommand(newStorageStatusCmd(), newStorageCleanCmd())
	return cmd
}

// newStorageStatusCmd creates the read-only "oro storage status" command.
func newStorageStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Oro storage health and usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			oroHome, err := resolveOroHome()
			if err != nil {
				return fmt.Errorf("resolve Oro home: %w", err)
			}
			status, err := loadStorageStatus(cmd.Context(), oroHome)
			if err != nil {
				return err
			}
			return writeStorageStatus(cmd.OutOrStdout(), status, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit storage status as JSON")
	return cmd
}

type storageCleanupOutput struct {
	Scope          storage.Scope                 `json:"scope"`
	Apply          bool                          `json:"apply"`
	CatalogHealthy bool                          `json:"catalog_healthy"`
	Decisions      []storageCleanupDecision      `json:"decisions"`
	Providers      []storage.MaintenanceEvidence `json:"providers,omitempty"`
	FreedBytes     uint64                        `json:"freed_bytes,omitempty"`
	FreeBytes      uint64                        `json:"free_bytes,omitempty"`
}

type storageCleanupDecision struct {
	Path           string                 `json:"path"`
	Scope          storage.Scope          `json:"scope"`
	Action         storage.ActionType     `json:"action"`
	PreserveReason storage.PreserveReason `json:"preserve_reason,omitempty"`
	Reason         storage.RetentionClass `json:"reason,omitempty"`
	BeforeBytes    int64                  `json:"before_bytes"`
	AfterBytes     int64                  `json:"after_bytes"`
	Changed        bool                   `json:"changed"`
}

type storageCleanDependencies struct {
	providers              []storage.CacheProvider
	runProviderMaintenance storage.ProviderMaintenanceRunner
}

func defaultStorageCleanDependencies() storageCleanDependencies {
	return storageCleanDependencies{
		providers:              storage.BuiltinProviders(),
		runProviderMaintenance: storage.RunProviderMaintenance,
	}
}

func newStorageCleanCmd() *cobra.Command {
	return newStorageCleanCmdWithDependencies(defaultStorageCleanDependencies())
}

func newStorageCleanCmdWithDependencies(dependencies storageCleanDependencies) *cobra.Command {
	var scopeValue string
	var apply bool
	var dryRun bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Plan scoped storage cleanup",
		Long:  "Plans cleanup from the storage catalog without modifying files. With --scope dev-tools, --apply runs trusted provider maintenance and reports cleanup evidence.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if apply && dryRun {
				return fmt.Errorf("--apply and --dry-run cannot be used together")
			}
			scope, err := parseStorageCleanupScope(scopeValue)
			if err != nil {
				return err
			}
			result, err := runStorageCleanWithDependencies(cmd.Context(), scope, apply, dependencies)
			if err != nil {
				return err
			}
			return writeStorageCleanup(cmd.OutOrStdout(), result, jsonOut)
		},
	}
	cmd.Flags().StringVar(&scopeValue, "scope", string(storage.ScopeAll), "cleanup scope: all, runtime, worktrees, oro-home, or dev-tools")
	cmd.Flags().BoolVar(&apply, "apply", false, "remove candidates proven safe by the cleanup plan")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "explicitly preview cleanup without modifying files")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit cleanup plan as JSON")
	return cmd
}

func parseStorageCleanupScope(value string) (storage.Scope, error) {
	scope := storage.Scope(strings.TrimSpace(value))
	switch scope {
	case storage.ScopeAll, storage.ScopeRuntime, storage.ScopeWorktrees, storage.ScopeOroHome, storage.ScopeDevTools:
		return scope, nil
	default:
		return "", fmt.Errorf("invalid storage cleanup scope %q", value)
	}
}

func runStorageClean(ctx context.Context, oroHome string, scope storage.Scope, apply bool) (storageCleanupOutput, error) {
	return runStorageCleanAtHome(ctx, oroHome, scope, apply)
}

func runStorageCleanWithDependencies(ctx context.Context, scope storage.Scope, apply bool, dependencies storageCleanDependencies) (storageCleanupOutput, error) {
	if scope == storage.ScopeDevTools {
		return runDevToolsStorageClean(ctx, apply, dependencies)
	}
	oroHome, err := resolveOroHome()
	if err != nil {
		return storageCleanupOutput{}, fmt.Errorf("resolve Oro home: %w", err)
	}
	return runStorageCleanAtHome(ctx, oroHome, scope, apply)
}

func runStorageCleanAtHome(ctx context.Context, oroHome string, scope storage.Scope, apply bool) (storageCleanupOutput, error) {
	// Moved here from runStorageClean by the oro-dev-cli merge: this function now
	// owns the body the scope check guarded, and both entry points route through it.
	if scope == storage.ScopeOroHome {
		return runOroHomeCleanup(ctx, oroHome, apply)
	}
	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		return storageCleanupOutput{}, fmt.Errorf("resolve storage paths: %w", err)
	}
	snapshot := loadStorageCleanupSnapshot(ctx, paths.CatalogPath)
	plan := storage.PlanCleanup(snapshot, storage.StoragePolicy{DeletionAuthorized: apply}, scope)
	if apply {
		if err := applyStorageCleanup(plan); err != nil {
			return storageCleanupOutput{}, err
		}
	}
	result := storageCleanupOutput{
		Scope:          scope,
		Apply:          apply,
		CatalogHealthy: snapshot.CatalogHealthy,
		Decisions:      storageCleanupDecisions(plan),
	}
	if scope != storage.ScopeAll {
		return result, nil
	}
	homeResult, err := runOroHomeCleanup(ctx, oroHome, apply)
	if err != nil {
		return storageCleanupOutput{}, err
	}
	result.Decisions = append(result.Decisions, homeResult.Decisions...)
	return result, nil
}

func runOroHomeCleanup(ctx context.Context, oroHome string, apply bool) (storageCleanupOutput, error) {
	result, err := storage.CleanOroHome(ctx, oroHome, apply)
	if err != nil {
		return storageCleanupOutput{}, fmt.Errorf("clean Oro home: %w", err)
	}
	return storageCleanupOutput{
		Scope:     storage.ScopeOroHome,
		Apply:     apply,
		Decisions: oroHomeCleanupDecisions(result.Entries, apply),
	}, nil
}

func runDevToolsStorageClean(ctx context.Context, apply bool, dependencies storageCleanDependencies) (storageCleanupOutput, error) {
	result := storageCleanupOutput{Scope: storage.ScopeDevTools, Apply: apply}
	if !apply {
		return result, nil
	}
	cleanup, err := storage.RunDevToolsCleanup(ctx, storage.DevToolsCleanupRequest{
		Providers: dependencies.providers,
		Run:       dependencies.runProviderMaintenance,
	})
	result.Providers = cleanup.Providers
	result.FreedBytes = cleanup.FreedBytes
	result.FreeBytes = cleanup.FreeBytes
	if err != nil {
		return result, err
	}
	return result, nil
}

func loadStorageCleanupSnapshot(ctx context.Context, catalogPath string) storage.Snapshot {
	db, err := openReadOnlyCatalog(ctx, catalogPath)
	if err != nil {
		return storage.Snapshot{}
	}
	defer func() { _ = db.Close() }()
	if !storageCatalogHealthy(ctx, db) {
		return storage.Snapshot{}
	}
	candidates, err := storageCleanupCandidates(ctx, db)
	if err != nil {
		return storage.Snapshot{}
	}
	return storage.Snapshot{CatalogHealthy: true, Candidates: candidates}
}

func storageCatalogHealthy(ctx context.Context, db *sql.DB) bool {
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return false
	}
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return false
	}
	return storageCatalogPragmasHealthy(integrity, version) && validateStorageCatalog(ctx, db) == nil
}

func storageCleanupCandidates(ctx context.Context, db *sql.DB) ([]storage.Candidate, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT n.path, EXISTS(
			SELECT 1 FROM leases l WHERE l.namespace_id = n.id AND l.expires_at > ?
		)
		FROM namespaces n
		ORDER BY n.id`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("list cleanup namespaces: %w", err)
	}
	defer rows.Close()
	candidates := make([]storage.Candidate, 0)
	for rows.Next() {
		var candidate storage.Candidate
		if err := rows.Scan(&candidate.Path, &candidate.LeaseActive); err != nil {
			return nil, fmt.Errorf("scan cleanup namespace: %w", err)
		}
		candidate.Scope = storage.ScopeRuntime
		candidate.Allowlisted = true
		candidate.Owned = true
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cleanup namespaces: %w", err)
	}
	return candidates, nil
}

func applyStorageCleanup(plan storage.Plan) error {
	for _, decision := range plan.Decisions {
		if decision.Action != storage.Delete {
			continue
		}
		if err := os.RemoveAll(decision.Candidate.Path); err != nil {
			return fmt.Errorf("remove planned storage path %s: %w", decision.Candidate.Path, err)
		}
	}
	return nil
}

func storageCleanupDecisions(plan storage.Plan) []storageCleanupDecision {
	decisions := make([]storageCleanupDecision, 0, len(plan.Decisions))
	for _, decision := range plan.Decisions {
		decisions = append(decisions, storageCleanupDecision{
			Path:           decision.Candidate.Path,
			Scope:          decision.Candidate.Scope,
			Action:         decision.Action,
			PreserveReason: decision.PreserveReason,
		})
	}
	return decisions
}

func oroHomeCleanupDecisions(entries []storage.OroHomeCleanupEntry, apply bool) []storageCleanupDecision {
	decisions := make([]storageCleanupDecision, 0, len(entries))
	for _, entry := range entries {
		action := storage.Preserve
		if apply {
			action = storage.Delete
		}
		decisions = append(decisions, storageCleanupDecision{
			Path:        entry.Path,
			Scope:       storage.ScopeOroHome,
			Action:      action,
			Reason:      entry.Reason,
			BeforeBytes: entry.BeforeBytes,
			AfterBytes:  entry.AfterBytes,
			Changed:     entry.Changed,
		})
	}
	return decisions
}

func writeStorageCleanup(w io.Writer, result storageCleanupOutput, jsonOut bool) error {
	if jsonOut {
		if err := json.NewEncoder(w).Encode(result); err != nil {
			return fmt.Errorf("encode storage cleanup: %w", err)
		}
		return nil
	}
	if result.Scope == storage.ScopeDevTools {
		if _, err := fmt.Fprintf(w, "Freed %.2f GiB — now %.2f GiB free\n", bytesToGiB(result.FreedBytes), bytesToGiB(result.FreeBytes)); err != nil {
			return fmt.Errorf("write dev-tools cleanup summary: %w", err)
		}
		return nil
	}
	for _, decision := range result.Decisions {
		if decision.Reason != "" {
			if _, err := fmt.Fprintf(w, "%s %s (%s; before=%d after=%d changed=%t)\n", decision.Action, decision.Path, decision.Reason, decision.BeforeBytes, decision.AfterBytes, decision.Changed); err != nil {
				return fmt.Errorf("write storage cleanup: %w", err)
			}
			continue
		}
		if decision.Action == storage.Delete {
			if _, err := fmt.Fprintf(w, "%s %s\n", decision.Action, decision.Path); err != nil {
				return fmt.Errorf("write storage cleanup: %w", err)
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s %s (%s)\n", decision.Action, decision.Path, decision.PreserveReason); err != nil {
			return fmt.Errorf("write storage cleanup: %w", err)
		}
	}
	return nil
}

func bytesToGiB(bytes uint64) float64 {
	return float64(bytes) / float64(uint64(1)<<30)
}

func loadStorageStatus(ctx context.Context, oroHome string) (storageStatus, error) {
	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		return storageStatus{}, fmt.Errorf("resolve storage paths: %w", err)
	}
	status, err := storageFilesystemStatus(paths.OroHome)
	if err != nil {
		return storageStatus{}, err
	}
	status.Bytes.Catalog, err = storageCatalogBytes(paths.CatalogPath)
	if err != nil {
		return storageStatus{}, err
	}
	status.Bytes.Evidence, err = storagePathBytes(paths.EvidenceRoot)
	if err != nil {
		return storageStatus{}, err
	}
	status.Bytes.Cache, err = storagePathBytes(paths.CacheRoot)
	if err != nil {
		return storageStatus{}, err
	}
	status.Bytes.Total = status.Bytes.Catalog + status.Bytes.Evidence + status.Bytes.Cache

	return loadStorageCatalogStatus(ctx, paths.CatalogPath, status), nil
}

func loadFactoryStorageHealth(ctx context.Context, oroHome string) *factoryhealth.StorageHealth {
	status, err := loadStorageStatus(ctx, oroHome)
	if err != nil || status.Catalog.Health != "healthy" {
		return &factoryhealth.StorageHealth{}
	}
	return &factoryhealth.StorageHealth{
		Available:    true,
		Pressure:     status.Pressure,
		SweepOverdue: status.DevCleanup.OverdueBySeconds > 0,
		AdmissionPaused: status.DevCleanup.Pause.State == storage.PauseRequested ||
			status.DevCleanup.Pause.State == storage.Paused ||
			status.DevCleanup.Pause.State == storage.Resuming,
		DevCleanup: &status.DevCleanup,
	}
}

func storageSweepOverdue(status storageStatus, now time.Time) bool {
	if status.NextSweep == "" {
		return false
	}
	nextSweep, err := time.Parse(time.RFC3339, status.NextSweep)
	return err == nil && !nextSweep.After(now)
}

func storageFilesystemStatus(path string) (storageStatus, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return storageStatus{}, fmt.Errorf("inspect storage filesystem: %w", err)
	}
	blockSize := uint64(stat.Bsize)
	status := storageStatus{
		CapacityBytes: stat.Blocks * blockSize,
		FreeBytes:     stat.Bavail * blockSize,
	}
	status.Pressure = storagePressure(status.CapacityBytes, status.FreeBytes)
	return status, nil
}

func storagePressure(capacity, free uint64) string {
	const gib = uint64(1024 * 1024 * 1024)
	warning := max(capacity/10, 50*gib)
	critical := max(capacity/20, 20*gib)
	switch {
	case free < critical:
		return "critical"
	case free < warning:
		return "warning"
	default:
		return "normal"
	}
}

func storagePathBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if errorsIsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect storage path %s: %w", path, err)
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	walkRoot, err := filepath.EvalSymlinks(path)
	if err != nil {
		return 0, fmt.Errorf("resolve storage path %s: %w", path, err)
	}
	var size int64
	err = filepath.WalkDir(walkRoot, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			entryInfo, err := entry.Info()
			if err != nil {
				return fmt.Errorf("read storage entry info: %w", err)
			}
			size += entryInfo.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure storage path %s: %w", path, err)
	}
	return size, nil
}

func storageCatalogBytes(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		size, err := storagePathBytes(path + suffix)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

func loadStorageCatalogStatus(ctx context.Context, path string, status storageStatus) storageStatus {
	if _, err := os.Stat(path); errorsIsNotExist(err) {
		status.Catalog.Health = "preservation_mode"
		return status
	} else if err != nil {
		status.Catalog.Health = "unavailable"
		return status
	}

	db, err := openReadOnlyCatalog(ctx, path)
	if err != nil {
		status.Catalog.Health = "corrupt"
		return status
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		status.Catalog.Health = "corrupt"
		return status
	}
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		status.Catalog.Health = "corrupt"
		return status
	}
	if !storageCatalogPragmasHealthy(integrity, version) {
		status.Catalog.Health = "corrupt"
		return status
	}
	if err := validateStorageCatalog(ctx, db); err != nil {
		status.Catalog.Health = "corrupt"
		return status
	}
	if err := loadStorageCatalogCounts(ctx, db, &status); err != nil {
		status.Catalog.Health = "corrupt"
		return status
	}
	status.Catalog.Health = "healthy"
	return status
}

func storageCatalogPragmasHealthy(integrity string, version int) bool {
	return integrity == "ok" && version == storage.CatalogSchemaVersion
}

func validateStorageCatalog(ctx context.Context, db *sql.DB) error {
	requiredTables := []string{
		"providers",
		"namespaces",
		"leases",
		"controllers",
		"refs",
		"sweeps",
		"evidence",
		"weekly_dev_cache_schedule",
		"runtime_leases",
		"runtime_controllers",
		"runtime_pause_epochs",
		"runtime_pause_acknowledgements",
		"runtime_tombstones",
		"runtime_reconciliation_cursors",
	}
	for _, table := range requiredTables {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			return fmt.Errorf("inspect catalog table %s: %w", table, err)
		}
		if count != 1 {
			return fmt.Errorf("catalog table %s missing", table)
		}
	}

	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check catalog foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("catalog foreign key violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog foreign key check: %w", err)
	}

	return nil
}

func openReadOnlyCatalog(ctx context.Context, path string) (*sql.DB, error) {
	catalogURL := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", catalogURL)
	if err != nil {
		return nil, fmt.Errorf("open catalog read-only: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping catalog read-only: %w", err)
	}
	return db, nil
}

func loadStorageCatalogCounts(ctx context.Context, db *sql.DB, status *storageStatus) error {
	rows, err := db.QueryContext(ctx, `SELECT expires_at FROM leases`)
	if err != nil {
		return fmt.Errorf("list catalog leases: %w", err)
	}
	defer rows.Close()
	now := time.Now().UTC()
	for rows.Next() {
		var expiresAt string
		if err := rows.Scan(&expiresAt); err != nil {
			return fmt.Errorf("scan catalog lease: %w", err)
		}
		expires, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return fmt.Errorf("parse catalog lease: %w", err)
		}
		if expires.After(now) {
			status.Leases.Active++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog leases: %w", err)
	}
	if err := loadStorageSweepStatus(ctx, db, status); err != nil {
		return err
	}
	return loadStorageDevCleanupStatus(ctx, db, status, time.Now().UTC())
}

func loadStorageDevCleanupStatus(ctx context.Context, db *sql.DB, status *storageStatus, now time.Time) error {
	var dueAt string
	err := db.QueryRowContext(ctx, `SELECT due_at FROM weekly_dev_cache_schedule WHERE id = 'weekly-dev-cache'`).Scan(&dueAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load weekly developer cleanup due time: %w", err)
	}
	if err == nil {
		due, parseErr := time.Parse(time.RFC3339, dueAt)
		if parseErr != nil {
			return fmt.Errorf("parse weekly developer cleanup due time: %w", parseErr)
		}
		status.DevCleanup.NextDue = due.Format(time.RFC3339)
		if now.After(due) {
			status.DevCleanup.OverdueBySeconds = int64(now.Sub(due).Seconds())
		}
	}

	rows, err := db.QueryContext(ctx, `
SELECT s.provider_id, s.status, s.finished_at, e.payload
FROM sweeps s
JOIN evidence e ON e.sweep_id = s.id AND e.kind = 'weekly_dev_cache_provider'
ORDER BY s.provider_id, s.finished_at DESC`)
	if err != nil {
		return fmt.Errorf("list weekly developer cleanup providers: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	for rows.Next() {
		var providerID, sweepStatus string
		var finishedAt sql.NullString
		var payload string
		if err := rows.Scan(&providerID, &sweepStatus, &finishedAt, &payload); err != nil {
			return fmt.Errorf("scan weekly developer cleanup provider: %w", err)
		}
		if _, ok := seen[providerID]; ok {
			continue
		}
		seen[providerID] = struct{}{}
		var evidence storage.MaintenanceEvidence
		if err := json.Unmarshal([]byte(payload), &evidence); err != nil {
			return fmt.Errorf("parse weekly developer cleanup evidence: %w", err)
		}
		result := storage.DevCleanupProviderResult{
			ProviderID: providerID,
			Status:     sweepStatus,
			ExitCode:   evidence.ExitCode,
		}
		if finishedAt.Valid && finishedAt.String != "" {
			finished, parseErr := time.Parse(time.RFC3339, finishedAt.String)
			if parseErr != nil {
				return fmt.Errorf("parse weekly developer cleanup attempt: %w", parseErr)
			}
			result.AttemptedAt = finished.Format(time.RFC3339)
			if status.DevCleanup.LastAttempt == "" || result.AttemptedAt > status.DevCleanup.LastAttempt {
				status.DevCleanup.LastAttempt = result.AttemptedAt
			}
			if sweepStatus == "completed" && (status.DevCleanup.LastSuccess == "" || result.AttemptedAt > status.DevCleanup.LastSuccess) {
				status.DevCleanup.LastSuccess = result.AttemptedAt
			}
		}
		freed := int64(evidence.Before.UsedBytes) - int64(evidence.After.UsedBytes)
		if freed > 0 {
			result.FreedBytes = freed
			status.DevCleanup.FreedBytes += freed
		}
		status.DevCleanup.Providers = append(status.DevCleanup.Providers, result)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate weekly developer cleanup providers: %w", err)
	}
	return loadStorageDevCleanupPause(ctx, db, status)
}

func loadStorageDevCleanupPause(ctx context.Context, db *sql.DB, status *storageStatus) error {
	var pause storage.DevCleanupPauseStatus
	err := db.QueryRowContext(ctx, `SELECT epoch, state FROM runtime_pause_epochs ORDER BY epoch DESC LIMIT 1`).Scan(&pause.Epoch, &pause.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load weekly developer cleanup pause: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_pause_acknowledgements WHERE epoch = ?`, pause.Epoch).Scan(&pause.AcknowledgedControllers); err != nil {
		return fmt.Errorf("count weekly developer cleanup acknowledgements: %w", err)
	}
	pause.Drained = pause.State == storage.Paused && pause.AcknowledgedControllers > 0
	status.DevCleanup.Pause = pause
	return nil
}

func loadStorageSweepStatus(ctx context.Context, db *sql.DB, status *storageStatus) error {
	rows, err := db.QueryContext(ctx, `SELECT status, finished_at FROM sweeps`)
	if err != nil {
		return fmt.Errorf("list catalog sweeps: %w", err)
	}
	defer rows.Close()
	var last time.Time
	for rows.Next() {
		var sweepStatus string
		var finishedAt sql.NullString
		if err := rows.Scan(&sweepStatus, &finishedAt); err != nil {
			return fmt.Errorf("scan catalog sweep: %w", err)
		}
		if !finishedAt.Valid || finishedAt.String == "" {
			status.Backlog.PendingSweeps++
			continue
		}
		if sweepStatus != "completed" {
			continue
		}
		finished, err := time.Parse(time.RFC3339, finishedAt.String)
		if err != nil {
			return fmt.Errorf("parse catalog sweep: %w", err)
		}
		if finished.After(last) {
			last = finished
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog sweeps: %w", err)
	}
	if !last.IsZero() {
		status.LastSweep = last.Format(time.RFC3339)
		status.NextSweep = last.Add(weeklyStorageSweepInterval).Format(time.RFC3339)
	}
	return nil
}

func writeStorageStatus(w io.Writer, status storageStatus, jsonOut bool) error {
	if jsonOut {
		encoder := json.NewEncoder(w)
		if err := encoder.Encode(status); err != nil {
			return fmt.Errorf("encode storage status: %w", err)
		}
		return nil
	}
	_, err := fmt.Fprintf(w, "storage: %s\nfree: %d / %d bytes\nusage: %d bytes\ncatalog: %s\nactive leases: %d\npending sweeps: %d\nnext sweep: %s\n",
		status.Pressure,
		status.FreeBytes,
		status.CapacityBytes,
		status.Bytes.Total,
		status.Catalog.Health,
		status.Leases.Active,
		status.Backlog.PendingSweeps,
		status.NextSweep,
	)
	if err != nil {
		return fmt.Errorf("write storage status: %w", err)
	}
	return nil
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
