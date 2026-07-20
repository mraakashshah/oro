package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"
)

const (
	weeklyStorageSweepInterval     = 7 * 24 * time.Hour
	supportedStorageCatalogVersion = 1
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
	LastSweep string `json:"last_sweep,omitempty"`
	NextSweep string `json:"next_sweep,omitempty"`
}

// newStorageCmd creates the read-only storage command group.
func newStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Inspect Oro-managed storage",
	}
	cmd.AddCommand(newStorageStatusCmd())
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
	return integrity == "ok" && version == supportedStorageCatalogVersion
}

func validateStorageCatalog(ctx context.Context, db *sql.DB) error {
	requiredTables := []string{"providers", "namespaces", "leases", "controllers", "refs", "sweeps", "evidence"}
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
	return loadStorageSweepStatus(ctx, db, status)
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
