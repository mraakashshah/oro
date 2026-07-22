package worker //nolint:testpackage // verifies the unexported message-dispatch failure contract.

import (
	"testing"

	"oro/pkg/protocol"
)

func TestCapabilityRefreshFailureIsNonFatal(t *testing.T) {
	w := &Worker{}
	done, err := w.handleMessage(t.Context(), protocol.Message{
		Type: protocol.MsgCapabilityRefresh,
		CapabilityRefresh: &protocol.CapabilityRefreshPayload{
			AssignmentID: 1,
			Generation:   1,
			CapabilityID: "stale-capability",
			Capability:   "stale-token",
		},
	})
	if err != nil {
		t.Fatalf("stale capability refresh stopped worker: %v", err)
	}
	if done {
		t.Fatal("stale capability refresh requested shutdown")
	}
}
