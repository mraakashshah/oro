//nolint:testpackage // white-box lifecycle ordering assertions
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestReviewReleaseCauseValues(t *testing.T) {
	want := map[ReviewReleaseCause]string{
		ReviewReleaseCauseConnectionLost: "connection_lost",
		ReviewReleaseCauseSendFailed:     "send_failed",
		ReviewReleaseCauseKilled:         "killed",
		ReviewReleaseCauseRestarted:      "restarted",
	}
	if len(want) != 4 {
		t.Fatalf("release cause cardinality = %d, want 4", len(want))
	}
	for cause, value := range want {
		if string(cause) != value {
			t.Fatalf("release cause %q = %q, want %q", cause, cause, value)
		}
	}
}

func TestReleaseCheckpointOwnedWorkerDurableBeforeMemory(t *testing.T) {
	ctx := context.Background()

	t.Run("success deletes exact worker then emits one event and wake", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "success")
		calls := 0
		releaseFn := func(_ context.Context, beadID, workerID string) (bool, error) {
			calls++
			if beadID != expected.beadID || workerID != expected.id {
				t.Fatalf("durable release identity = %q/%q, want %q/%q", beadID, workerID, expected.beadID, expected.id)
			}
			if got := trackedReleaseWorker(d, expected.id); got != expected {
				t.Fatalf("worker changed before durable release: got %p, want %p", got, expected)
			}
			assertCheckpointReleaseEventCount(t, d, 0)
			assertNoCheckpointReleaseWake(t, d)
			return true, nil
		}

		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseSendFailed, releaseFn)
		if err != nil || !released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (true, nil)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != nil {
			t.Fatalf("released worker remains tracked: %p", got)
		}
		assertCheckpointReleaseEvent(t, d, expected.beadID, expected.id, ReviewReleaseCauseSendFailed)
		assertOneCheckpointReleaseWake(t, d)
	})

	t.Run("durable no-op preserves memory without event or wake", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "no-op")
		calls := 0
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				calls++
				return false, nil
			})
		if err != nil || released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("durable no-op changed worker: got %p, want %p", got, expected)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("durable error preserves memory without event or wake", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "error")
		wantErr := errors.New("injected durable release failure")
		calls := 0
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseKilled,
			func(context.Context, string, string) (bool, error) {
				calls++
				return false, wantErr
			})
		if released || !errors.Is(err, wantErr) {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (false, injected error)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		if got := trackedReleaseWorker(d, expected.id); got != expected {
			t.Fatalf("durable error changed worker: got %p, want %p", got, expected)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("reconnect during durable release keeps replacement", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "reconnect")
		replacement := &trackedWorker{
			id:     expected.id,
			beadID: "replacement-bead",
			conn:   newMockConn(),
		}
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseRestarted,
			func(context.Context, string, string) (bool, error) {
				d.mu.Lock()
				d.workers[expected.id] = replacement
				d.mu.Unlock()
				return true, nil
			})
		if err != nil || !released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (true, nil)", released, err)
		}
		if got := trackedReleaseWorker(d, expected.id); got != replacement {
			t.Fatalf("replacement worker changed: got %p, want %p", got, replacement)
		}
		assertCheckpointReleaseEvent(t, d, expected.beadID, expected.id, ReviewReleaseCauseRestarted)
		assertOneCheckpointReleaseWake(t, d)
	})

	t.Run("stale expected pointer does not call durable store", func(t *testing.T) {
		d, live := newCheckpointWorkerReleaseFixture(t, "stale")
		stale := &trackedWorker{id: live.id, beadID: live.beadID, conn: live.conn}
		calls := 0
		released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, stale, ReviewReleaseCauseConnectionLost,
			func(context.Context, string, string) (bool, error) {
				calls++
				return true, nil
			})
		if err != nil || released {
			t.Fatalf("releaseCheckpointOwnedWorkerUsing() = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 0 {
			t.Fatalf("durable release calls = %d, want 0", calls)
		}
		if got := trackedReleaseWorker(d, live.id); got != live {
			t.Fatalf("stale release changed live worker: got %p, want %p", got, live)
		}
		assertCheckpointReleaseEventCount(t, d, 0)
		assertNoCheckpointReleaseWake(t, d)
	})

	t.Run("repeated release is idempotent", func(t *testing.T) {
		d, expected := newCheckpointWorkerReleaseFixture(t, "repeat")
		calls := 0
		releaseFn := func(context.Context, string, string) (bool, error) {
			calls++
			return true, nil
		}
		if released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseKilled, releaseFn); err != nil || !released {
			t.Fatalf("first release = (%t, %v), want (true, nil)", released, err)
		}
		assertOneCheckpointReleaseWake(t, d)

		if released, err := d.releaseCheckpointOwnedWorkerUsing(ctx, expected, ReviewReleaseCauseKilled, releaseFn); err != nil || released {
			t.Fatalf("repeated release = (%t, %v), want (false, nil)", released, err)
		}
		if calls != 1 {
			t.Fatalf("durable release calls = %d, want 1", calls)
		}
		assertCheckpointReleaseEventCount(t, d, 1)
		assertNoCheckpointReleaseWake(t, d)
	})
}

func newCheckpointWorkerReleaseFixture(t *testing.T, suffix string) (*Dispatcher, *trackedWorker) {
	t.Helper()
	d, _, _, _, _, _ := newTestDispatcher(t)
	worker := &trackedWorker{
		id:     "checkpoint-release-worker-" + suffix,
		beadID: "checkpoint-release-bead-" + suffix,
		conn:   newMockConn(),
	}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	drainCheckpointReleaseWakes(d)
	return d, worker
}

func trackedReleaseWorker(d *Dispatcher, workerID string) *trackedWorker {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.workers[workerID]
}

func assertCheckpointReleaseEvent(t *testing.T, d *Dispatcher, beadID, workerID string, cause ReviewReleaseCause) {
	t.Helper()
	var gotBeadID, gotWorkerID, payload string
	if err := d.db.QueryRow(`SELECT bead_id, worker_id, payload FROM events
		WHERE type='review_checkpoint_worker_released' ORDER BY id`).Scan(&gotBeadID, &gotWorkerID, &payload); err != nil {
		t.Fatalf("load release event: %v", err)
	}
	var got struct {
		Cause ReviewReleaseCause `json:"cause"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("decode release event payload %q: %v", payload, err)
	}
	if gotBeadID != beadID || gotWorkerID != workerID || got.Cause != cause {
		t.Fatalf("release event = bead %q worker %q cause %q, want %q/%q/%q",
			gotBeadID, gotWorkerID, got.Cause, beadID, workerID, cause)
	}
	assertCheckpointReleaseEventCount(t, d, 1)
}

func assertCheckpointReleaseEventCount(t *testing.T, d *Dispatcher, want int) {
	t.Helper()
	var got int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='review_checkpoint_worker_released'`).Scan(&got); err != nil {
		t.Fatalf("count release events: %v", err)
	}
	if got != want {
		t.Fatalf("release event count = %d, want %d", got, want)
	}
}

func assertOneCheckpointReleaseWake(t *testing.T, d *Dispatcher) {
	t.Helper()
	select {
	case <-d.workerReadyCh:
	default:
		t.Fatal("checkpoint worker release did not wake assignment")
	}
	assertNoCheckpointReleaseWake(t, d)
}

func assertNoCheckpointReleaseWake(t *testing.T, d *Dispatcher) {
	t.Helper()
	select {
	case <-d.workerReadyCh:
		t.Fatal("unexpected checkpoint worker release assignment wake")
	default:
	}
}

func drainCheckpointReleaseWakes(d *Dispatcher) {
	for {
		select {
		case <-d.workerReadyCh:
		default:
			return
		}
	}
}
