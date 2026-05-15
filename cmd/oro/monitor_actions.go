package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (r *cliMonitorRunner) RecentMonitorAction(ctx context.Context, action, key string, window time.Duration) (bool, error) {
	db, err := openMonitorActionDB()
	if err != nil {
		return false, err
	}
	defer db.Close()
	return recentMonitorAction(ctx, db, action, key, window)
}

func (r *cliMonitorRunner) RecordMonitorAction(ctx context.Context, action monitorAction) error {
	db, err := openMonitorActionDB()
	if err != nil {
		return err
	}
	defer db.Close()
	return recordMonitorAction(ctx, db, action)
}

func openMonitorActionDB() (*sql.DB, error) {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve monitor action db path: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return nil, fmt.Errorf("open monitor action db: %w", err)
	}
	return db, nil
}

func recentMonitorAction(ctx context.Context, db *sql.DB, action, key string, window time.Duration) (bool, error) {
	if db == nil || action == "" || key == "" {
		return false, nil
	}
	if window <= 0 {
		window = monitorActionDedupeWindow
	}
	modifier := fmt.Sprintf("-%d seconds", int(window.Seconds()))
	var count int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM monitor_actions
 WHERE action = ?
   AND action_key = ?
   AND created_at >= datetime('now', ?)`,
		action, key, modifier).Scan(&count); err != nil {
		return false, fmt.Errorf("query monitor action ledger: %w", err)
	}
	return count > 0, nil
}

func recordMonitorAction(ctx context.Context, db *sql.DB, action monitorAction) error {
	if db == nil || action.Action == "" || action.Key == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO monitor_actions (action, action_key, payload)
VALUES (?, ?, ?)`,
		action.Action, action.Key, action.Payload); err != nil {
		return fmt.Errorf("insert monitor action ledger row: %w", err)
	}
	return nil
}
