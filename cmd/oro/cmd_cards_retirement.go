package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const memoryRetirementWindow = 14 * 24 * time.Hour

// MemoryRetirementReadiness reports whether legacy pkg/memory can be retired.
type MemoryRetirementReadiness struct {
	Ready       bool
	Blockers    []string
	LiveImports []MemoryRetirementImport
}

// MemoryRetirementImport is a production import of oro/pkg/memory that blocks retirement.
type MemoryRetirementImport struct {
	Path string
}

// newMemoryRetirementCheckCmd creates the "oro cards memory-retirement-check"
// readiness gate for retiring the legacy memory package.
func newMemoryRetirementCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "memory-retirement-check",
		Short: "Check whether legacy memory is ready to retire",
		Long: "Fails closed until memory read telemetry is older than 14 days\n" +
			"and no production code imports oro/pkg/memory outside the retirement allowlist.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveProjectDBPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			db, err := openStateDB(paths.StateDBPath)
			if err != nil {
				return fmt.Errorf("open state db: %w", err)
			}
			defer func() { _ = db.Close() }()

			report, err := EvaluateMemoryRetirementReadiness(cmd.Context(), db, time.Now(), currentRepoRoot())
			if err != nil {
				return err
			}
			if report.Ready {
				fmt.Fprintln(cmd.OutOrStdout(), "ready: legacy memory retirement gate passed")
				return nil
			}
			for _, blocker := range report.Blockers {
				fmt.Fprintf(cmd.OutOrStdout(), "BLOCKED: %s\n", blocker)
			}
			for _, liveImport := range report.LiveImports {
				fmt.Fprintf(cmd.OutOrStdout(), "LIVE_IMPORT: %s\n", liveImport.Path)
			}
			return errors.New("memory retirement gate is not ready")
		},
	}
}

// EvaluateMemoryRetirementReadiness checks telemetry and source imports without
// performing CLI I/O.
func EvaluateMemoryRetirementReadiness(ctx context.Context, db *sql.DB, now time.Time, scanRoot string) (MemoryRetirementReadiness, error) {
	var report MemoryRetirementReadiness
	if strings.TrimSpace(scanRoot) == "" {
		return report, errors.New("scan root is required")
	}

	report.Blockers = append(report.Blockers, evaluateMemoryReadTelemetry(ctx, db, now)...)

	liveImports, err := scanLiveMemoryImports(scanRoot)
	if err != nil {
		return report, err
	}
	report.LiveImports = liveImports
	if len(liveImports) > 0 {
		report.Blockers = append(report.Blockers, fmt.Sprintf("live pkg/memory imports remain: %d", len(liveImports)))
	}

	report.Ready = len(report.Blockers) == 0
	return report, nil
}

func evaluateMemoryReadTelemetry(ctx context.Context, db *sql.DB, now time.Time) []string {
	if db == nil {
		return []string{"telemetry database unavailable"}
	}
	cutoff := now.UTC().Add(-memoryRetirementWindow).Format("2006-01-02 15:04:05")

	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_read_events`).Scan(&total); err != nil {
		return []string{fmt.Sprintf("memory read telemetry unavailable: %v", err)}
	}
	if total == 0 {
		return []string{"no memory read telemetry found"}
	}

	var recent int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_read_events WHERE ts >= ?`, cutoff).Scan(&recent); err != nil {
		return []string{fmt.Sprintf("memory read telemetry unavailable: %v", err)}
	}
	if recent > 0 {
		return []string{fmt.Sprintf("recent memory reads inside 14-day retirement window: %d", recent)}
	}
	return nil
}

func scanLiveMemoryImports(root string) ([]MemoryRetirementImport, error) {
	root = filepath.Clean(root)
	var imports []MemoryRetirementImport
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if isMemoryRetirementIgnoredDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("scan %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		if isMemoryRetirementImportAllowlisted(rel) {
			return nil
		}

		hasImport, err := fileImportsMemory(path)
		if err != nil {
			return fmt.Errorf("scan %s: %w", rel, err)
		}
		if hasImport {
			imports = append(imports, MemoryRetirementImport{Path: rel})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk scan root: %w", err)
	}
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Path < imports[j].Path
	})
	return imports, nil
}

func isMemoryRetirementIgnoredDir(name string) bool {
	return name == "vendor" || name == ".git" || name == worktreesDirName
}

func isMemoryRetirementImportAllowlisted(path string) bool {
	return path == "cmd/oro/cmd_cards_check_drift.go" || path == "pkg/cards/legacy_writer.go"
}

func fileImportsMemory(path string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return false, fmt.Errorf("parse imports: %w", err)
	}
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) == "oro/pkg/memory" {
			return true, nil
		}
	}
	return false, nil
}
