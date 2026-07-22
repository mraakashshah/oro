package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestControllerPauseLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if _, err := storage.NewController(storage.ControllerConfig{ID: "controller"}); err == nil {
		t.Fatal("NewController() with nil catalog error = nil, want error")
	}

	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if _, err := storage.NewController(storage.ControllerConfig{Catalog: catalog}); err == nil {
		t.Fatal("NewController() with empty ID error = nil, want error")
	}
	if _, err := storage.NewController(storage.ControllerConfig{Catalog: catalog, ID: "controller"}); err == nil {
		t.Fatal("NewController() with nil Drain error = nil, want error")
	}
	if _, err := storage.NewController(storage.ControllerConfig{
		Catalog: catalog,
		ID:      "controller",
		Drain:   func(context.Context) error { return nil },
	}); err == nil {
		t.Fatal("NewController() without durable registration error = nil, want error")
	}

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "controller", OwnerID: "owner", PID: 101, ProcessStart: now.Add(-time.Minute), HeartbeatAt: now.Add(-time.Second),
		Identity: storage.ProcessIdentity{PID: 101, StartMarker: "start", Executable: "oro", ProcessGroup: 101},
	}); err != nil {
		t.Fatalf("upsert controller: %v", err)
	}
	drainErr := errors.New("active work remains")
	drainCalls := 0
	usage := storage.Usage{}
	probeCalls := 0
	var cancelProbe context.CancelFunc
	config := storage.ControllerConfig{
		Catalog: catalog,
		ID:      "controller",
		Drain: func(context.Context) error {
			drainCalls++
			if drainCalls == 1 {
				return drainErr
			}
			return nil
		},
		Probe: func(context.Context) (storage.Usage, error) {
			probeCalls++
			if cancelProbe != nil {
				cancelProbe()
				cancelProbe = nil
			}
			return usage, nil
		},
		WarningFreeBytes: 50 << 30,
	}
	controller, err := storage.NewController(config)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	if !controller.Admit() {
		t.Fatal("Admit() before pause = false, want true")
	}
	initialObservation := now.Add(-500 * time.Millisecond)
	if err := controller.Observe(ctx, initialObservation); err != nil {
		t.Fatalf("Observe() without pause epoch error = %v", err)
	}
	registered, err := catalog.Controller(ctx, "controller")
	if err != nil {
		t.Fatalf("load initial controller observation: %v", err)
	}
	if !registered.HeartbeatAt.Equal(initialObservation) {
		t.Fatalf("initial controller heartbeat = %s, want %s", registered.HeartbeatAt, initialObservation)
	}

	protocol := storage.NewPauseEpochProtocol(catalog, nil)
	pause, err := protocol.RequestPause(ctx, now)
	if err != nil {
		t.Fatalf("request pause: %v", err)
	}
	if err := controller.Observe(ctx, now); !errors.Is(err, drainErr) {
		t.Fatalf("Observe() drain error = %v, want %v", err, drainErr)
	}
	if controller.Admit() {
		t.Fatal("Admit() after pause request = true, want false")
	}
	if _, err := catalog.PauseAcknowledgement(ctx, pause.Epoch, "controller"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("acknowledgement after failed drain error = %v, want sql.ErrNoRows", err)
	}
	observed, err := catalog.Controller(ctx, "controller")
	if err != nil {
		t.Fatalf("load observed controller: %v", err)
	}
	if observed.ObservedEpoch != pause.Epoch || !observed.HeartbeatAt.Equal(now) {
		t.Fatalf("observed controller epoch/heartbeat = %d/%s, want %d/%s", observed.ObservedEpoch, observed.HeartbeatAt, pause.Epoch, now)
	}

	if err := controller.Observe(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("Observe() successful drain error = %v", err)
	}
	if _, err := catalog.PauseAcknowledgement(ctx, pause.Epoch, "controller"); err != nil {
		t.Fatalf("load acknowledgement after drain: %v", err)
	}
	if controller.Admit() {
		t.Fatal("Admit() after pause acknowledgement = true, want false")
	}
	restarted, err := storage.NewController(config)
	if err != nil {
		t.Fatalf("restart controller: %v", err)
	}
	if restarted.Admit() {
		t.Fatal("Admit() after restart during pause = true, want false")
	}

	resumingAt := now.Add(time.Minute)
	if err := catalog.RecordPauseEpoch(ctx, storage.PauseEpoch{
		Epoch:     pause.Epoch + 1,
		State:     storage.Resuming,
		CreatedAt: resumingAt,
	}); err != nil {
		t.Fatalf("record resuming epoch: %v", err)
	}
	usage = storage.Usage{ScratchBytes: storage.ScratchTargetBytes, FreeBytes: (50 << 30) + 1}
	if err := controller.Observe(ctx, resumingAt); err != nil {
		t.Fatalf("Observe() stale probe error = %v", err)
	}
	if probeCalls != 0 || controller.Admit() {
		t.Fatalf("stale probe calls/admission = %d/%t, want 0/false", probeCalls, controller.Admit())
	}

	usage.ScratchBytes = storage.ScratchTargetBytes + 1
	if err := controller.Observe(ctx, resumingAt.Add(time.Second)); err != nil {
		t.Fatalf("Observe() excessive scratch error = %v", err)
	}
	usage = storage.Usage{ScratchBytes: storage.ScratchTargetBytes, FreeBytes: 50 << 30}
	if err := controller.Observe(ctx, resumingAt.Add(2*time.Second)); err != nil {
		t.Fatalf("Observe() insufficient free bytes error = %v", err)
	}
	if controller.Admit() {
		t.Fatal("Admit() after unhealthy probes = true, want false")
	}

	usage.FreeBytes++
	firstHealthy := resumingAt.Add(3 * time.Second)
	if err := controller.Observe(ctx, firstHealthy); err != nil {
		t.Fatalf("Observe() first healthy probe error = %v", err)
	}
	if controller.Admit() {
		t.Fatal("Admit() after one healthy probe = true, want false")
	}
	if err := controller.Observe(ctx, firstHealthy.Add(29*time.Second)); err != nil {
		t.Fatalf("Observe() too-close probe error = %v", err)
	}
	if controller.Admit() {
		t.Fatal("Admit() after too-close healthy probe = true, want false")
	}
	probeCtx, cancel := context.WithCancel(ctx)
	cancelProbe = cancel
	if err := controller.Observe(probeCtx, firstHealthy.Add(30*time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Observe() canceled healthy probe error = %v, want context.Canceled", err)
	}
	if controller.Admit() {
		t.Fatal("Admit() after canceled healthy probe = true, want false")
	}
	if err := controller.Observe(ctx, firstHealthy.Add(30*time.Second)); err != nil {
		t.Fatalf("Observe() second spaced probe error = %v", err)
	}
	if !controller.Admit() {
		t.Fatal("Admit() after two spaced healthy probes = false, want true")
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("close catalog before fail-closed observation: %v", err)
	}
	if err := controller.Observe(ctx, firstHealthy.Add(time.Minute)); err == nil {
		t.Fatal("Observe() with unavailable catalog error = nil, want error")
	}
	if controller.Admit() {
		t.Fatal("Admit() after observation failure = true, want false")
	}
}
