package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// SwarmHealth represents the health status of all oro components.
type SwarmHealth struct {
	Daemon      DaemonStatus   `json:"daemon"`
	ManagerPane PaneStatus     `json:"manager_pane"`
	Workers     []workerStatus `json:"workers"`
}

// DaemonStatus represents the health of the dispatcher daemon.
type DaemonStatus struct {
	PID           int     `json:"pid"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	State         string  `json:"state"`
}

// PaneStatus represents the health of a tmux pane.
type PaneStatus struct {
	Name         string `json:"name"`
	Alive        bool   `json:"alive"`
	LastActivity string `json:"last_activity,omitempty"` // ISO 8601 timestamp
}

// paneAlive reports whether the given pane has a pane_activity row whose
// last_seen unix timestamp is within the past 60 seconds of nowUnix.
func paneAlive(ctx context.Context, db *sql.DB, nowUnix int64) bool {
	var lastSeen int64
	err := db.QueryRowContext(ctx, `SELECT last_seen FROM pane_activity WHERE pane = 'manager'`).Scan(&lastSeen)
	if err != nil {
		return false
	}
	return nowUnix-lastSeen <= 60
}

func (d *Dispatcher) maybeRecoverDolt(ctx context.Context) {
	_ = ctx
}

// applyHealth returns a JSON representation of the swarm health status.
// It includes daemon status, pane statuses, and worker statuses.
func (d *Dispatcher) applyHealth() (string, error) {
	now := d.nowFunc()
	nowUnix := now.Unix()

	managerAlive := paneAlive(context.Background(), d.db, nowUnix)

	d.mu.Lock()
	workers, _, _, _ := d.snapshotWorkers(now)

	health := SwarmHealth{
		Daemon: DaemonStatus{
			PID:           os.Getpid(),
			UptimeSeconds: now.Sub(d.startTime).Seconds(),
			State:         string(d.state),
		},
		ManagerPane: PaneStatus{
			Name:  "manager",
			Alive: managerAlive,
		},
		Workers: workers,
	}
	d.mu.Unlock()

	data, err := json.Marshal(health)
	if err != nil {
		return "", fmt.Errorf("marshal health: %w", err)
	}
	return string(data), nil
}
