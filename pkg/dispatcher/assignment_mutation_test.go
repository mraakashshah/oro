package dispatcher //nolint:testpackage // targeted white-box tests exercise assignment coordination contracts

import (
	"context"
	"slices"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestAssignBeadWithClaimReportsUnclaimedValidationFailure(t *testing.T) {
	d := &Dispatcher{}
	worker := &trackedWorker{id: "mutation-claim-worker"}
	var claims []bool
	var outcomes []assignmentSetupOutcome

	err := d.assignBeadWithClaim(
		context.Background(),
		worker,
		protocol.Bead{},
		nil,
		func(claimed bool) { claims = append(claims, claimed) },
		func(outcome assignmentSetupOutcome) { outcomes = append(outcomes, outcome) },
	)
	if err == nil {
		t.Fatal("assignBeadWithClaim accepted an empty bead ID")
	}
	if !slices.Equal(claims, []bool{false}) {
		t.Fatalf("claim callbacks = %v, want one unclaimed result", claims)
	}
	if !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) {
		t.Fatalf("outcome callbacks = %v, want one not-delivered result", outcomes)
	}
}

func TestReleaseAssignmentReservationResetsStateAndUnlocks(t *testing.T) {
	const (
		workerID       = "mutation-release-worker"
		beadID         = "mutation-release-bead"
		reservationGen = uint64(7)
	)
	worker := &trackedWorker{
		id:             workerID,
		state:          protocol.WorkerReserved,
		beadID:         beadID,
		reservationGen: reservationGen,
		assignmentID:   42,
		worktree:       "/tmp/mutation-release-worktree",
	}
	d := &Dispatcher{
		WorkerPool: WorkerPool{workers: map[string]*trackedWorker{workerID: worker}},
		nowFunc:    time.Now,
	}

	d.releaseAssignmentReservation(workerID, beadID, reservationGen)

	if worker.state != protocol.WorkerIdle || worker.beadID != "" || worker.assignmentID != 0 ||
		worker.worktree != "" || worker.reservationGen != reservationGen+1 {
		t.Fatalf("released worker retained reservation state: %+v", worker)
	}
	lockAvailable := make(chan struct{})
	go func() {
		d.mu.Lock()
		d.mu.Unlock() //nolint:staticcheck // lock/unlock completion is the bounded mutex-release assertion
		close(lockAvailable)
	}()
	select {
	case <-lockAvailable:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("releaseAssignmentReservation returned while retaining the dispatcher mutex")
	}
}
