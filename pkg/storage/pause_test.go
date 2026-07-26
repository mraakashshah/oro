package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestPauseEpochAcknowledgementProtocol(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	controllerOne := pauseTestController("dispatcher", 101, now)
	controllerTwo := pauseTestController("standalone", 202, now)
	for _, controller := range []storage.Controller{controllerOne, controllerTwo} {
		if err := catalog.UpsertController(ctx, controller); err != nil {
			t.Fatalf("upsert controller %s: %v", controller.ID, err)
		}
	}

	inspector := func(pid int) (storage.ProcessIdentity, error) {
		switch pid {
		case controllerOne.PID:
			return controllerOne.Identity, nil
		case controllerTwo.PID:
			return controllerTwo.Identity, nil
		default:
			return storage.ProcessIdentity{}, errors.New("process exited")
		}
	}
	protocol := storage.NewPauseEpochProtocol(catalog, inspector)

	first, err := protocol.RequestPause(ctx, now)
	if err != nil {
		t.Fatalf("request first pause: %v", err)
	}
	if first.Epoch != 1 {
		t.Fatalf("first epoch = %d, want 1", first.Epoch)
	}
	for _, controller := range []storage.Controller{controllerOne, controllerTwo} {
		if err := protocol.Acknowledge(ctx, first.Epoch, controller.ID, now); err != nil {
			t.Fatalf("acknowledge first epoch for %s: %v", controller.ID, err)
		}
	}
	if err := protocol.Acknowledge(ctx, first.Epoch, controllerOne.ID, now.Add(time.Second)); !errors.Is(err, storage.ErrPauseEpochAlreadyAcknowledged) {
		t.Fatalf("duplicate acknowledgement error = %v, want ErrPauseEpochAlreadyAcknowledged", err)
	}
	if acknowledged, err := protocol.Acknowledged(ctx, first.Epoch, now); err != nil || !acknowledged {
		t.Fatalf("first epoch acknowledged = %t, %v; want true, nil", acknowledged, err)
	}

	second, err := protocol.RequestPause(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("request second pause: %v", err)
	}
	if second.Epoch != first.Epoch+1 {
		t.Fatalf("second epoch = %d, want %d", second.Epoch, first.Epoch+1)
	}
	if acknowledged, err := protocol.Acknowledged(ctx, second.Epoch, now.Add(time.Minute)); err != nil || acknowledged {
		t.Fatalf("stale acknowledgement satisfied newer pause = %t, %v; want false, nil", acknowledged, err)
	}

	if err := protocol.Acknowledge(ctx, second.Epoch, controllerOne.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("acknowledge second epoch for controller one: %v", err)
	}
	if acknowledged, err := protocol.Acknowledged(ctx, second.Epoch, now.Add(time.Minute)); err != nil || acknowledged {
		t.Fatalf("live unacknowledged controller satisfied pause = %t, %v; want false, nil", acknowledged, err)
	}

	protocol = storage.NewPauseEpochProtocol(catalog, func(pid int) (storage.ProcessIdentity, error) {
		if pid == controllerOne.PID {
			return controllerOne.Identity, nil
		}
		return storage.ProcessIdentity{}, errors.New("process exited")
	})
	if acknowledged, err := protocol.Acknowledged(ctx, second.Epoch, now.Add(time.Minute)); err != nil || !acknowledged {
		t.Fatalf("crashed controller did not expire by identity = %t, %v; want true, nil", acknowledged, err)
	}
}

func TestPauseEpochProtocolRejectsControllerWithoutIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	controller := pauseTestController("dispatcher", 101, now)
	controller.Identity = storage.ProcessIdentity{}
	if err := catalog.UpsertController(ctx, controller); err == nil {
		t.Fatal("upsert controller without identity error = nil, want error")
	}
}

func pauseTestController(id string, pid int, now time.Time) storage.Controller {
	return storage.Controller{
		ID:            id,
		OwnerID:       "owner-" + id,
		PID:           pid,
		ProcessStart:  now.Add(-time.Minute),
		ObservedEpoch: 0,
		HeartbeatAt:   now,
		Identity: storage.ProcessIdentity{
			PID:          pid,
			StartMarker:  "start-" + id,
			Executable:   "oro",
			ProcessGroup: pid,
		},
	}
}
