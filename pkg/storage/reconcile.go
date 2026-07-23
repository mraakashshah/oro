package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const legacyReconcileEntryLimit = 1024

// LegacyEntry describes one legacy scratch-root child returned by a paged source.
//
//oro:testonly — recurring maintenance wiring supplies the filesystem source.
type LegacyEntry struct {
	Name  string
	IsDir bool
}

// LegacyEntryPage is one ordered page from a legacy scratch-root source.
//
//oro:testonly — recurring maintenance wiring supplies the filesystem source.
type LegacyEntryPage struct {
	Entries []LegacyEntry
}

// LegacyEntrySource reads ordered legacy scratch entries strictly after a cursor.
// Implementations must page rather than materializing the scratch root.
//
//oro:testonly — recurring maintenance wiring supplies the filesystem source.
type LegacyEntrySource interface {
	ReadPage(context.Context, string, string, int) (LegacyEntryPage, error)
}

// LegacyReconcileResult reports one bounded legacy scratch scan.
//
//oro:testonly — recurring maintenance wiring lands with the recurring storage triggers.
type LegacyReconcileResult struct {
	Examined int
	Adopted  int
	Cursor   string
	Complete bool
}

// LegacyReconciler scans one configured legacy scratch root through a paged source.
//
//oro:testonly — recurring maintenance wiring lands with the recurring storage triggers.
type LegacyReconciler struct {
	catalog *Catalog
	root    string
	source  LegacyEntrySource
}

// NewLegacyReconciler creates a bounded legacy scratch reconciler.
//
//oro:testonly — recurring maintenance wiring lands with the recurring storage triggers.
func NewLegacyReconciler(catalog *Catalog, root string, source LegacyEntrySource) *LegacyReconciler {
	return &LegacyReconciler{catalog: catalog, root: root, source: source}
}

// Reconcile reads no more than one bounded page and a one-entry completion
// probe. It keeps the prior durable cursor when the source cannot be read.
//
//oro:testonly — recurring maintenance wiring lands with the recurring storage triggers.
func (r *LegacyReconciler) Reconcile(ctx context.Context) (LegacyReconcileResult, error) {
	if err := r.validate(); err != nil {
		return LegacyReconcileResult{}, err
	}
	root, err := canonicalCachePath(r.root)
	if err != nil {
		return LegacyReconcileResult{}, fmt.Errorf("resolve legacy scratch root: %w", err)
	}
	if _, err := safeDirectory(root); err != nil {
		return LegacyReconcileResult{}, fmt.Errorf("validate legacy scratch root: %w", err)
	}
	cursor, err := r.loadCursor(ctx, root)
	if err != nil {
		return LegacyReconcileResult{}, err
	}
	page, err := r.readPage(ctx, root, cursor, legacyReconcileEntryLimit)
	if err != nil {
		return LegacyReconcileResult{}, err
	}
	result, err := r.processPage(ctx, root, page)
	if err != nil {
		return LegacyReconcileResult{}, err
	}
	result.Complete, err = r.pageComplete(ctx, root, result.Cursor, len(page.Entries))
	if err != nil {
		return LegacyReconcileResult{}, err
	}
	if result.Complete {
		result.Cursor = ""
	}
	if err := r.catalog.SaveReconciliationCursor(ctx, ReconciliationCursor{
		Name:      r.cursorNameForRoot(root),
		Cursor:    result.Cursor,
		Proof:     "preservation-only",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return LegacyReconcileResult{}, fmt.Errorf("save legacy scratch cursor: %w", err)
	}
	return result, nil
}

func (r *LegacyReconciler) readPage(ctx context.Context, root, after string, limit int) (LegacyEntryPage, error) {
	page, err := r.source.ReadPage(ctx, root, after, limit)
	if err != nil {
		return LegacyEntryPage{}, fmt.Errorf("read legacy scratch page: %w", err)
	}
	if err := validateLegacyPage(page, after, limit); err != nil {
		return LegacyEntryPage{}, err
	}
	return page, nil
}

func (r *LegacyReconciler) processPage(ctx context.Context, root string, page LegacyEntryPage) (LegacyReconcileResult, error) {
	result := LegacyReconcileResult{Examined: len(page.Entries)}
	for _, entry := range page.Entries {
		if err := ctx.Err(); err != nil {
			return LegacyReconcileResult{}, fmt.Errorf("reconcile legacy scratch context: %w", err)
		}
		result.Cursor = entry.Name
		adopted, err := r.adopt(ctx, root, entry)
		if err != nil {
			return LegacyReconcileResult{}, err
		}
		if adopted {
			result.Adopted++
		}
	}
	return result, nil
}

func (r *LegacyReconciler) pageComplete(ctx context.Context, root, cursor string, entries int) (bool, error) {
	if entries < legacyReconcileEntryLimit {
		return true, nil
	}
	probe, err := r.readPage(ctx, root, cursor, 1)
	if err != nil {
		return false, fmt.Errorf("probe legacy scratch completion: %w", err)
	}
	return len(probe.Entries) == 0, nil
}

func (r *LegacyReconciler) validate() error {
	if r == nil || r.catalog == nil || r.root == "" || r.source == nil {
		return fmt.Errorf("invalid legacy reconciler")
	}
	return nil
}

func (r *LegacyReconciler) cursorName() string {
	root, err := canonicalCachePath(r.root)
	if err != nil {
		return r.cursorNameForRoot(r.root)
	}
	return r.cursorNameForRoot(root)
}

func (r *LegacyReconciler) cursorNameForRoot(root string) string {
	sum := sha256.Sum256([]byte(root))
	return fmt.Sprintf("legacy-scratch:%x", sum[:])
}

func (r *LegacyReconciler) loadCursor(ctx context.Context, root string) (string, error) {
	cursor, err := r.catalog.ReconciliationCursor(ctx, r.cursorNameForRoot(root))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load legacy scratch cursor: %w", err)
	}
	return cursor.Cursor, nil
}

func validateLegacyPage(page LegacyEntryPage, after string, limit int) error {
	if len(page.Entries) > limit {
		return fmt.Errorf("legacy scratch page exceeds limit %d", limit)
	}
	previous := after
	for _, entry := range page.Entries {
		if entry.Name == "" || entry.Name <= previous {
			return fmt.Errorf("legacy scratch page is not strictly ordered after cursor")
		}
		previous = entry.Name
	}
	return nil
}

func (r *LegacyReconciler) adopt(ctx context.Context, root string, entry LegacyEntry) (bool, error) {
	if !entry.IsDir || !namespaceTokenPattern.MatchString(entry.Name) {
		return false, nil
	}
	path := filepath.Join(root, entry.Name)
	info, err := os.Lstat(path) //nolint:gosec // G304: path is the named direct child of the validated configured scratch root.
	if err != nil {
		return false, fmt.Errorf("inspect legacy namespace %s: %w", entry.Name, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}

	var exists bool
	err = r.catalog.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_leases WHERE namespace = ? AND scratch_path = ?)`, entry.Name, path).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check legacy namespace %s: %w", entry.Name, err)
	}
	if exists {
		return false, nil
	}
	now := time.Now().UTC()
	_, err = r.catalog.db.ExecContext(ctx, `INSERT INTO runtime_leases (id, namespace, scratch_path, controller_id, owner_id, pid, process_start, acquired_at, heartbeat_at, released_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacy:"+entry.Name, entry.Name, path, "legacy-reconcile", "legacy-reconcile", 1, formatTime(now), formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return false, fmt.Errorf("adopt legacy namespace %s: %w", entry.Name, err)
	}
	return true, nil
}
