package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	controllerInitTimeout = 5 * time.Second
	resumeProbeInterval   = 30 * time.Second
)

// ControllerConfig supplies the durable state and injected lifecycle edges for
// one dispatcher or standalone controller.
type ControllerConfig struct {
	Catalog          *Catalog
	ID               string
	Drain            func(context.Context) error
	Probe            func(context.Context) (Usage, error)
	WarningFreeBytes int64
}

type controllerRuntime struct {
	catalog          *Catalog
	protocol         *PauseEpochProtocol
	drain            func(context.Context) error
	probe            func(context.Context) (Usage, error)
	warningFreeBytes int64

	observeMu sync.Mutex
	stateMu   sync.RWMutex
	admitted  bool
	epoch     int64
	state     PauseState
	drained   int64
	resumed   int64
	healthyAt time.Time
}

// NewController creates a controller that follows the catalog's latest pause
// epoch.
//
//oro:testonly — dispatcher and standalone wiring lands in dependent controller tasks.
func NewController(config ControllerConfig) (*Controller, error) {
	if config.Catalog == nil {
		return nil, fmt.Errorf("nil controller catalog")
	}
	if strings.TrimSpace(config.ID) == "" {
		return nil, fmt.Errorf("empty controller ID")
	}
	if config.Drain == nil {
		return nil, fmt.Errorf("nil controller drain")
	}
	if config.Probe == nil {
		config.Probe = func(context.Context) (Usage, error) { return Usage{}, nil }
	}
	initCtx, cancel := context.WithTimeout(context.Background(), controllerInitTimeout)
	defer cancel()

	runtime := &controllerRuntime{
		catalog:          config.Catalog,
		protocol:         NewPauseEpochProtocol(config.Catalog, nil),
		drain:            config.Drain,
		probe:            config.Probe,
		warningFreeBytes: config.WarningFreeBytes,
	}
	epoch, err := config.Catalog.latestPauseEpoch(initCtx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		runtime.admitted = true
		runtime.state = Open
	case err != nil:
		return nil, err
	default:
		switch epoch.State {
		case Open:
			runtime.admitted = true
		case PauseRequested, Paused, Resuming:
			runtime.admitted = false
		default:
			return nil, fmt.Errorf("unsupported pause state %q", epoch.State)
		}
		runtime.epoch = epoch.Epoch
		runtime.state = epoch.State
	}
	record, err := config.Catalog.Controller(initCtx, config.ID)
	if err != nil {
		return nil, fmt.Errorf("load controller %s registration: %w", config.ID, err)
	}
	record.runtime = runtime

	return &record, nil
}

// Observe applies the newest durable pause state at observedAt.
//
//oro:testonly — dispatcher and standalone wiring lands in dependent controller tasks.
func (c *Controller) Observe(ctx context.Context, observedAt time.Time) error {
	if c == nil || c.runtime == nil {
		return fmt.Errorf("invalid controller")
	}
	if observedAt.IsZero() {
		return fmt.Errorf("zero controller observation time")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("observe controller context: %w", err)
	}

	runtime := c.runtime
	runtime.observeMu.Lock()
	defer runtime.observeMu.Unlock()

	epoch, err := runtime.catalog.latestPauseEpoch(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		epoch = PauseEpoch{State: Open, CreatedAt: observedAt}
		if err := c.persistObservation(ctx, epoch, observedAt); err != nil {
			runtime.failClosed()
			return err
		}
		runtime.open(epoch)
		return nil
	}
	if err != nil {
		runtime.failClosed()
		return err
	}
	if epoch.State != Open {
		runtime.close(epoch)
	}
	if err := c.persistObservation(ctx, epoch, observedAt); err != nil {
		runtime.failClosed()
		return err
	}

	switch epoch.State {
	case Open:
		runtime.open(epoch)
		return nil
	case PauseRequested:
		return c.observePauseRequested(ctx, epoch, observedAt)
	case Paused:
		runtime.close(epoch)
		return nil
	case Resuming:
		return c.observeResuming(ctx, epoch, observedAt)
	default:
		runtime.close(epoch)
		return fmt.Errorf("unsupported pause state %q", epoch.State)
	}
}

func (c *Controller) persistObservation(ctx context.Context, epoch PauseEpoch, observedAt time.Time) error {
	record, err := c.runtime.catalog.Controller(ctx, c.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("controller %s registration missing: %w", c.ID, err)
	}
	if err != nil {
		return fmt.Errorf("load controller %s observation record: %w", c.ID, err)
	}
	record.ObservedEpoch = epoch.Epoch
	record.HeartbeatAt = observedAt
	if err := c.runtime.catalog.UpsertController(ctx, record); err != nil {
		return fmt.Errorf("persist controller %s observation: %w", c.ID, err)
	}
	c.ObservedEpoch = epoch.Epoch
	c.HeartbeatAt = observedAt
	return nil
}

func (c *Controller) observePauseRequested(ctx context.Context, epoch PauseEpoch, observedAt time.Time) error {
	runtime := c.runtime
	runtime.close(epoch)
	if runtime.drainComplete(epoch.Epoch) {
		return nil
	}

	if _, err := runtime.catalog.PauseAcknowledgement(ctx, epoch.Epoch, c.ID); err == nil {
		runtime.markDrained(epoch.Epoch)
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if err := runtime.drain(ctx); err != nil {
		return fmt.Errorf("drain controller %s: %w", c.ID, err)
	}
	if err := runtime.protocol.Acknowledge(ctx, epoch.Epoch, c.ID, observedAt); err != nil && !errors.Is(err, ErrPauseEpochAlreadyAcknowledged) {
		return fmt.Errorf("acknowledge controller %s pause: %w", c.ID, err)
	}
	runtime.markDrained(epoch.Epoch)
	return nil
}

func (c *Controller) observeResuming(ctx context.Context, epoch PauseEpoch, observedAt time.Time) error {
	runtime := c.runtime
	runtime.close(epoch)
	if runtime.resumeComplete(epoch.Epoch) || !observedAt.After(epoch.CreatedAt) {
		return nil
	}

	usage, err := runtime.probe(ctx)
	if err != nil {
		return fmt.Errorf("probe controller %s storage usage: %w", c.ID, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("probe controller %s context: %w", c.ID, err)
	}
	runtime.applyProbe(epoch.Epoch, observedAt, usage)
	return nil
}

// Admit reports whether this controller may start new Oro-owned work.
//
//oro:testonly — dispatcher and standalone wiring lands in dependent controller tasks.
func (c *Controller) Admit() bool {
	if c == nil || c.runtime == nil {
		return false
	}
	c.runtime.stateMu.RLock()
	defer c.runtime.stateMu.RUnlock()
	return c.runtime.admitted
}

func (r *controllerRuntime) close(epoch PauseEpoch) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	changed := r.epoch != epoch.Epoch || r.state != epoch.State
	if r.epoch != epoch.Epoch {
		r.drained = 0
		r.resumed = 0
	}
	if changed {
		r.healthyAt = time.Time{}
	}
	r.epoch = epoch.Epoch
	r.state = epoch.State
	r.admitted = epoch.State == Resuming && r.resumed == epoch.Epoch
}

func (r *controllerRuntime) open(epoch PauseEpoch) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.epoch = epoch.Epoch
	r.state = epoch.State
	r.healthyAt = time.Time{}
	r.admitted = true
}

func (r *controllerRuntime) failClosed() {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.admitted = false
}

func (r *controllerRuntime) drainComplete(epoch int64) bool {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.drained == epoch
}

func (r *controllerRuntime) markDrained(epoch int64) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.drained = epoch
}

func (r *controllerRuntime) resumeComplete(epoch int64) bool {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.resumed == epoch
}

func (r *controllerRuntime) applyProbe(epoch int64, observedAt time.Time, usage Usage) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	healthy := usage.ScratchBytes >= 0 && usage.ScratchBytes <= ScratchTargetBytes &&
		usage.FreeBytes > r.warningFreeBytes
	if !healthy {
		r.healthyAt = time.Time{}
		return
	}
	if r.healthyAt.IsZero() {
		r.healthyAt = observedAt
		return
	}
	if !observedAt.Before(r.healthyAt.Add(resumeProbeInterval)) {
		r.resumed = epoch
		r.admitted = true
	}
}

func (c *Catalog) latestPauseEpoch(ctx context.Context) (PauseEpoch, error) {
	var value PauseEpoch
	var createdAt string
	err := c.db.QueryRowContext(ctx, `SELECT epoch, state, created_at FROM runtime_pause_epochs ORDER BY epoch DESC LIMIT 1`).Scan(&value.Epoch, &value.State, &createdAt)
	if err != nil {
		return PauseEpoch{}, fmt.Errorf("load latest pause epoch: %w", err)
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return PauseEpoch{}, fmt.Errorf("parse latest pause epoch %d: %w", value.Epoch, err)
	}
	return value, nil
}
