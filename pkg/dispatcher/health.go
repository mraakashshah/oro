package dispatcher

import (
	"encoding/json"
	"fmt"
	"os"
)

// SwarmHealth represents the health status of all oro components.
type SwarmHealth struct {
	Daemon        DaemonStatus   `json:"daemon"`
	ArchitectPane PaneStatus     `json:"architect_pane"`
	ManagerPane   PaneStatus     `json:"manager_pane"`
	Workers       []workerStatus `json:"workers"`
}

// DaemonStatus represents the health of the dispatcher daemon.
type DaemonStatus struct {
	PID           int     `json:"pid"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	State         string  `json:"state"`
}

// PaneStatus represents the health of a tmux pane (architect or manager).
type PaneStatus struct {
	Name         string `json:"name"`
	Alive        bool   `json:"alive"`
	LastActivity string `json:"last_activity,omitempty"` // ISO 8601 timestamp
}

// applyHealth returns a JSON representation of the swarm health status.
// It includes daemon status, pane statuses, and worker statuses.
func (d *Dispatcher) applyHealth() (string, error) {
	now := d.nowFunc()

	d.mu.Lock()
	workers, _, _, _ := d.snapshotWorkers(now)

	health := SwarmHealth{
		Daemon: DaemonStatus{
			PID:           os.Getpid(),
			UptimeSeconds: now.Sub(d.startTime).Seconds(),
			State:         string(d.state),
		},
		ArchitectPane: PaneStatus{
			Name:  "architect",
			Alive: false, // TODO: query tmux or pane_activity table
		},
		ManagerPane: PaneStatus{
			Name:  "manager",
			Alive: false, // TODO: query tmux or pane_activity table
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
