package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/web"
)

// DashboardData is the read-only query surface exposed to the web dashboard.
// All methods are safe for concurrent use.
type DashboardData interface {
	// Health returns the current swarm health status.
	Health() (SwarmHealth, error)
	// ReadyBeads returns beads that are ready for assignment.
	ReadyBeads(ctx context.Context) ([]protocol.Bead, error)
	// InProgressBeads returns beads currently assigned to workers.
	InProgressBeads(ctx context.Context) ([]protocol.Bead, error)
	// BlockedBeads returns beads blocked by unmet dependencies.
	BlockedBeads(ctx context.Context) ([]protocol.Bead, error)
	// ClosedBeads returns the most recently closed beads, up to limit.
	ClosedBeads(ctx context.Context, limit int) ([]protocol.Bead, error)
	// ShowBead returns extended detail for the given bead ID.
	ShowBead(ctx context.Context, id string) (*protocol.BeadDetail, error)
	// RecentEvents returns the n most recent events from the events table,
	// ordered by created_at DESC.
	RecentEvents(ctx context.Context, n int) ([]protocol.Event, error)
	// SubscribeSSE returns a channel that receives formatted SSE messages.
	// The caller must call UnsubscribeSSE when done to avoid leaking the channel.
	SubscribeSSE() chan string
	// UnsubscribeSSE deregisters the channel returned by SubscribeSSE.
	UnsubscribeSSE(ch chan string)
}

// Health implements DashboardData. It delegates to applyHealth and unmarshals
// the result into a SwarmHealth value.
func (d *Dispatcher) Health() (SwarmHealth, error) {
	raw, err := d.applyHealth()
	if err != nil {
		return SwarmHealth{}, fmt.Errorf("dashboard health: %w", err)
	}
	var h SwarmHealth
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return SwarmHealth{}, fmt.Errorf("dashboard health unmarshal: %w", err)
	}
	return h, nil
}

// ReadyBeads implements DashboardData.
func (d *Dispatcher) ReadyBeads(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := d.beads.Ready(ctx)
	if err != nil {
		return nil, fmt.Errorf("ready beads: %w", err)
	}
	return beads, nil
}

// InProgressBeads implements DashboardData.
func (d *Dispatcher) InProgressBeads(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := d.beads.InProgress(ctx)
	if err != nil {
		return nil, fmt.Errorf("in-progress beads: %w", err)
	}
	return beads, nil
}

// BlockedBeads implements DashboardData.
func (d *Dispatcher) BlockedBeads(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := d.beads.Blocked(ctx)
	if err != nil {
		return nil, fmt.Errorf("blocked beads: %w", err)
	}
	return beads, nil
}

// ClosedBeads implements DashboardData.
func (d *Dispatcher) ClosedBeads(ctx context.Context, limit int) ([]protocol.Bead, error) {
	beads, err := d.beads.Closed(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("closed beads: %w", err)
	}
	return beads, nil
}

// ShowBead implements DashboardData.
func (d *Dispatcher) ShowBead(ctx context.Context, id string) (*protocol.BeadDetail, error) {
	detail, err := d.beads.Show(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("show bead %s: %w", id, err)
	}
	return detail, nil
}

// RecentEvents implements DashboardData. It queries the events table ordered by
// created_at DESC, returning at most n rows. NULL columns (bead_id, worker_id,
// payload) are coerced to empty strings to avoid scan panics.
func (d *Dispatcher) RecentEvents(ctx context.Context, n int) ([]protocol.Event, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, type, source,
		       COALESCE(bead_id, ''),
		       COALESCE(worker_id, ''),
		       COALESCE(payload, ''),
		       created_at
		FROM events
		ORDER BY created_at DESC
		LIMIT ?
	`, n)
	if err != nil {
		return nil, fmt.Errorf("recent events query: %w", err)
	}
	defer rows.Close()

	events := make([]protocol.Event, 0)
	for rows.Next() {
		var e protocol.Event
		if err := rows.Scan(&e.ID, &e.Type, &e.Source, &e.BeadID, &e.WorkerID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("recent events scan: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent events rows: %w", err)
	}
	return events, nil
}

// HealthError implements web.DashboardData. It returns nil when the swarm is
// healthy, or a descriptive error when degraded.
func (d *Dispatcher) HealthError() error {
	h, err := d.Health()
	if err != nil {
		return err
	}
	if h.Daemon.State != "running" {
		return fmt.Errorf("daemon state: %s", h.Daemon.State)
	}
	return nil
}

// SubscribeSSE implements DashboardData.
func (d *Dispatcher) SubscribeSSE() chan string {
	return d.sseBroadcaster.Subscribe()
}

// UnsubscribeSSE implements DashboardData.
func (d *Dispatcher) UnsubscribeSSE(ch chan string) {
	d.sseBroadcaster.Unsubscribe(ch)
}

// Workers implements web.DashboardData. It snapshots the current worker
// states and returns them as a slice of web.WorkerInfo values.
func (d *Dispatcher) Workers(_ context.Context) ([]web.WorkerInfo, error) {
	d.mu.Lock()
	workers, _, _, _ := d.snapshotWorkers(d.nowFunc())
	d.mu.Unlock()

	result := make([]web.WorkerInfo, len(workers))
	for i, w := range workers {
		result[i] = web.WorkerInfo{
			ID:                w.ID,
			State:             w.State,
			BeadID:            w.BeadID,
			ContextPct:        w.ContextPct,
			LastHeartbeatSecs: w.LastHeartbeatSecs,
		}
	}
	return result, nil
}

// Throughput implements web.DashboardData. It returns basic swarm metrics.
func (d *Dispatcher) Throughput(_ context.Context) (*web.ThroughputData, error) {
	d.mu.Lock()
	workers, _, _, _ := d.snapshotWorkers(d.nowFunc())
	uptime := d.nowFunc().Sub(d.startTime).Round(time.Second)
	d.mu.Unlock()

	active := 0
	for _, w := range workers {
		if w.State == "busy" {
			active++
		}
	}

	hours := uptime.Hours()
	var uptimeStr string
	switch {
	case hours >= 1:
		uptimeStr = fmt.Sprintf("%.0fh %.0fm", hours, uptime.Minutes()-hours*60)
	default:
		uptimeStr = fmt.Sprintf("%.0fm", uptime.Minutes())
	}

	var beadsPerHour int
	if err := d.db.QueryRow(`
		SELECT COUNT(*)
		FROM events
		WHERE type = 'merged'
		  AND datetime(created_at) >= datetime('now', '-1 hour')
	`).Scan(&beadsPerHour); err != nil {
		return nil, fmt.Errorf("throughput merged count: %w", err)
	}

	return &web.ThroughputData{
		BeadsPerHour:  beadsPerHour,
		ActiveWorkers: active,
		TotalWorkers:  len(workers),
		Uptime:        uptimeStr,
		CostPerHour:   "—",
	}, nil
}
