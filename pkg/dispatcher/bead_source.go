package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// DeferredStore is the dispatcher-local extension for deferred bead repair.
type DeferredStore interface {
	beadstore.Store
	Defer(ctx context.Context, id, until string) error
	Undefer(ctx context.Context, id string) error
}

type dependencyStore interface {
	AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error
}

type dependencyRemovalStore interface {
	RemoveDependency(ctx context.Context, beadID, dependsOnID string) error
}

func selectStore(ctx context.Context, mode string, primary DeferredStore, db *sql.DB) (DeferredStore, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "cli":
		return primary, nil
	case "sqlite", "shadow":
		if db == nil {
			return nil, fmt.Errorf("select bead source %q: db is nil", mode)
		}
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			return nil, fmt.Errorf("select bead source %q: migrate bead schema: %w", mode, err)
		}
		if strings.EqualFold(strings.TrimSpace(mode), "shadow") {
			if _, err := db.ExecContext(ctx, protocol.MigrateKVStore); err != nil {
				return nil, fmt.Errorf("select bead source %q: migrate kv store: %w", mode, err)
			}
		}
		sqliteStore := beadstore.NewSQLiteStore(db)
		if strings.EqualFold(strings.TrimSpace(mode), "sqlite") {
			return sqliteStore, nil
		}
		shadowStartedAt, err := beadstore.LoadOrInitShadowStartedAt(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("select bead source %q: shadow started at: %w", mode, err)
		}
		return beadstore.NewShadowStore(primary, sqliteStore, beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
			logBeadstoreDivergence(ctx, db, event)
		}), beadstore.WithShadowStartedAt(shadowStartedAt)), nil
	default:
		return nil, fmt.Errorf("unknown %s %q", "ORO_BEADSOURCE_MODE", mode)
	}
}

func normalizeBeadSourceModeForPrimary(mode string, primary DeferredStore) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" && isSQLiteStore(primary) {
		return "sqlite"
	}
	if normalized == "sqlite" && !isSQLiteStore(primary) {
		return "cli"
	}
	return normalized
}

func isSQLiteStore(store DeferredStore) bool {
	_, ok := store.(*beadstore.SQLiteStore)
	return ok
}

func logBeadstoreDivergence(ctx context.Context, db *sql.DB, event beadstore.ShadowDivergence) {
	if db == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"operation": event.Operation,
		"kind":      string(event.Kind),
		"reason":    event.Reason,
	})
	if err != nil {
		return
	}
	_, _ = db.ExecContext(ctx,
		`INSERT INTO events (type, source, payload) VALUES (?, ?, ?)`,
		"beadstore_divergence", "beadstore_shadow", string(payload))
}

func updateBeadStatus(ctx context.Context, beads beadstore.Store, id, status string) error {
	if err := beads.Update(ctx, id, beadstore.UpdateParams{Status: &status}); err != nil {
		return fmt.Errorf("update bead %s status to %s: %w", id, status, err)
	}
	return nil
}
