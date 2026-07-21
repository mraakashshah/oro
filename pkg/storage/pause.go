package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrPauseEpochAlreadyAcknowledged reports a duplicate controller acknowledgement.
var ErrPauseEpochAlreadyAcknowledged = errors.New("pause epoch already acknowledged")

// ProcessInspector observes the immutable identity of a running process.
type ProcessInspector func(int) (ProcessIdentity, error)

// PauseEpochProtocol coordinates global pause epochs across Oro controllers.
type PauseEpochProtocol struct {
	catalog *Catalog
	inspect ProcessInspector
}

// NewPauseEpochProtocol creates a pause coordinator backed by catalog.
//
//oro:testonly — dispatcher and standalone admission wiring lands in dependent runtime lifecycle tasks.
func NewPauseEpochProtocol(catalog *Catalog, inspect ProcessInspector) *PauseEpochProtocol {
	if inspect == nil {
		inspect = InspectProcessIdentity
	}
	return &PauseEpochProtocol{catalog: catalog, inspect: inspect}
}

// RequestPause advances the durable global epoch and requests a new drain.
//
//oro:testonly — dispatcher and standalone admission wiring lands in dependent runtime lifecycle tasks.
func (p *PauseEpochProtocol) RequestPause(ctx context.Context, requestedAt time.Time) (PauseEpoch, error) {
	if p == nil || p.catalog == nil || requestedAt.IsZero() {
		return PauseEpoch{}, fmt.Errorf("invalid pause epoch protocol")
	}
	return p.catalog.nextPauseEpoch(ctx, PauseRequested, requestedAt)
}

// Acknowledge records one controller's completed drain for epoch.
func (p *PauseEpochProtocol) Acknowledge(ctx context.Context, epoch int64, controllerID string, acknowledgedAt time.Time) error {
	if p == nil || p.catalog == nil {
		return fmt.Errorf("invalid pause epoch protocol")
	}
	return p.catalog.AcknowledgePauseEpoch(ctx, PauseAcknowledgement{
		Epoch:          epoch,
		ControllerID:   controllerID,
		State:          Paused,
		AcknowledgedAt: acknowledgedAt,
	})
}

// Acknowledged reports whether every controller whose persisted process identity
// still matches a live process has acknowledged exactly this epoch.
//
//oro:testonly — dispatcher and standalone admission wiring lands in dependent runtime lifecycle tasks.
func (p *PauseEpochProtocol) Acknowledged(ctx context.Context, epoch int64, _ time.Time) (bool, error) {
	if p == nil || p.catalog == nil {
		return false, fmt.Errorf("invalid pause epoch protocol")
	}
	if _, err := p.catalog.PauseEpoch(ctx, epoch); err != nil {
		return false, err
	}
	controllers, err := p.catalog.controllers(ctx)
	if err != nil {
		return false, err
	}
	for _, controller := range controllers {
		if !p.live(controller) {
			continue
		}
		if _, err := p.catalog.PauseAcknowledgement(ctx, epoch, controller.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (p *PauseEpochProtocol) live(controller Controller) bool {
	identity, err := p.inspect(controller.PID)
	return err == nil && controller.Identity.Matches(identity)
}

func (c *Catalog) nextPauseEpoch(ctx context.Context, state PauseState, createdAt time.Time) (PauseEpoch, error) {
	if state == "" || createdAt.IsZero() {
		return PauseEpoch{}, fmt.Errorf("invalid pause epoch")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return PauseEpoch{}, fmt.Errorf("begin next pause epoch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(epoch), 0) + 1 FROM runtime_pause_epochs`).Scan(&epoch); err != nil {
		return PauseEpoch{}, fmt.Errorf("allocate pause epoch: %w", err)
	}
	value := PauseEpoch{Epoch: epoch, State: state, CreatedAt: createdAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_pause_epochs (epoch, state, created_at) VALUES (?, ?, ?)`, value.Epoch, value.State, formatTime(value.CreatedAt)); err != nil {
		return PauseEpoch{}, fmt.Errorf("record next pause epoch %d: %w", value.Epoch, err)
	}
	if err := tx.Commit(); err != nil {
		return PauseEpoch{}, fmt.Errorf("commit next pause epoch: %w", err)
	}
	return value, nil
}

func (c *Catalog) controllers(ctx context.Context) ([]Controller, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, owner_id, pid, process_start, identity_start, executable, process_group, observed_epoch, heartbeat_at FROM runtime_controllers`)
	if err != nil {
		return nil, fmt.Errorf("list controllers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	values := make([]Controller, 0)
	for rows.Next() {
		var value Controller
		var processStart, heartbeat string
		if err := rows.Scan(&value.ID, &value.OwnerID, &value.PID, &processStart, &value.Identity.StartMarker, &value.Identity.Executable, &value.Identity.ProcessGroup, &value.ObservedEpoch, &heartbeat); err != nil {
			return nil, fmt.Errorf("scan controller: %w", err)
		}
		var err error
		value.ProcessStart, err = parseTime(processStart)
		if err != nil {
			return nil, fmt.Errorf("parse controller %s process start: %w", value.ID, err)
		}
		value.HeartbeatAt, err = parseTime(heartbeat)
		if err != nil {
			return nil, fmt.Errorf("parse controller %s heartbeat: %w", value.ID, err)
		}
		value.Identity.PID = value.PID
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate controllers: %w", err)
	}
	return values, nil
}
