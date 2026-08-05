package dispatcher //nolint:testpackage // targeted white-box test exercises a bounded scheduling contract

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestLaunchAssignmentWithResultReportsDeclinedClaimWithinBound(t *testing.T) {
	d := &Dispatcher{}
	worker := &trackedWorker{id: "mutation-launch-worker"}
	returned := make(chan struct{})
	var claimed bool
	var setupDone <-chan struct{}
	var setupOutcome <-chan assignmentSetupOutcome
	go func() {
		claimed, setupDone, setupOutcome = d.launchAssignmentWithResult(
			context.Background(), worker, protocol.Bead{}, 0,
		)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("launchAssignmentWithResult did not report a declined claim within its bounded contract")
	}
	if claimed {
		t.Fatal("launchAssignmentWithResult claimed an empty bead")
	}
	select {
	case <-setupDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("launchAssignmentWithResult did not close setupDone after declining the claim")
	}
	select {
	case outcome, ok := <-setupOutcome:
		if !ok || outcome != assignmentSetupNotDelivered {
			t.Fatalf("setup outcome = (%v, %v), want (%v, true)", outcome, ok, assignmentSetupNotDelivered)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("launchAssignmentWithResult did not report setup outcome after declining the claim")
	}
}
