package dispatcher //nolint:testpackage // white-box test verifies tracked worker connection identity

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestProposalConnectionPreservesTrackedWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	trackedDispatcherConn, trackedWorkerConn := net.Pipe()
	t.Cleanup(func() { _ = trackedDispatcherConn.Close() })
	t.Cleanup(func() { _ = trackedWorkerConn.Close() })

	const workerID = "proposal-worker"
	d.registerWorker(workerID, trackedDispatcherConn)
	d.mu.Lock()
	tracked := d.workers[workerID]
	tracked.beadID = "proposal-bead"
	tracked.assignmentID = 41
	d.mu.Unlock()

	run := protocol.EvidenceRun{
		ID:           "evidence-connection",
		AssignmentID: 41,
		WorkerID:     workerID,
		BeadID:       "proposal-bead",
		Kind:         "diagnostic",
		Status:       "completed",
	}

	evidenceResponse := sendWorkRequest(t, d, protocol.Message{
		Type: protocol.MsgEvidenceRequest,
		EvidenceRequest: &protocol.EvidenceRequest{
			Evidence: run,
		},
	})
	if evidenceResponse.Type != protocol.MsgEvidenceResponse || evidenceResponse.EvidenceResponse == nil {
		t.Fatalf("evidence response = %#v, want typed evidence response", evidenceResponse)
	}
	if evidenceResponse.EvidenceResponse.Error != "" {
		t.Fatalf("evidence response error = %q", evidenceResponse.EvidenceResponse.Error)
	}

	executionResponse := sendWorkRequest(t, d, protocol.Message{
		Type: protocol.MsgEvidenceRequest,
		EvidenceRequest: &protocol.EvidenceRequest{
			Execution: &protocol.EvidenceExecutionRequest{
				AssignmentID: 999,
				WorkerID:     workerID,
				BeadID:       "proposal-bead",
				Kind:         "diagnostic",
				Argv:         []string{"printf", "evidence"},
				TimeoutMS:    int64(time.Second / time.Millisecond),
			},
		},
	})
	if executionResponse.Type != protocol.MsgEvidenceResponse || executionResponse.EvidenceResponse == nil {
		t.Fatalf("execution response = %#v, want typed evidence response", executionResponse)
	}
	if got := executionResponse.EvidenceResponse.Error; got != "run evidence: active assignment not found" {
		t.Fatalf("execution response error = %q, want execution-path assignment error", got)
	}

	proposalResponse := sendWorkRequest(t, d, protocol.Message{
		Type: protocol.MsgWorkProposalRequest,
		WorkProposalRequest: &protocol.WorkProposalRequest{
			Proposal: protocol.WorkProposalPayload{
				ClientProposalID:  "connection-proposal",
				AssignmentID:      run.AssignmentID,
				WorkerID:          run.WorkerID,
				BeadID:            run.BeadID,
				EvidenceRunID:     run.ID,
				Fingerprint:       "connection-fingerprint",
				Kind:              "prerequisite",
				Summary:           "need another task",
				SuggestedPriority: 2,
			},
		},
	})
	if proposalResponse.Type != protocol.MsgWorkProposalResponse || proposalResponse.WorkProposalResponse == nil {
		t.Fatalf("proposal response = %#v, want typed proposal response", proposalResponse)
	}
	if proposalResponse.WorkProposalResponse.Error != "" {
		t.Fatalf("proposal response error = %q", proposalResponse.WorkProposalResponse.Error)
	}
	if proposalResponse.WorkProposalResponse.Result.ProposalID == "" {
		t.Fatal("proposal response has empty proposal ID")
	}

	malformedResponse := sendWorkRequest(t, d, protocol.Message{
		Type:            protocol.MsgEvidenceRequest,
		EvidenceRequest: &protocol.EvidenceRequest{Evidence: protocol.EvidenceRun{}},
	})
	if malformedResponse.Type != protocol.MsgEvidenceResponse || malformedResponse.EvidenceResponse == nil {
		t.Fatalf("malformed evidence response = %#v, want typed evidence response", malformedResponse)
	}
	if malformedResponse.EvidenceResponse.Error == "" {
		t.Fatal("malformed evidence request did not return an error response")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	got := d.workers[workerID]
	if got != tracked {
		t.Fatalf("tracked worker = %p, want original %p", got, tracked)
	}
	if got.conn != trackedDispatcherConn {
		t.Fatal("proposal connection replaced the tracked worker connection")
	}
	if got.beadID != "proposal-bead" || got.assignmentID != 41 {
		t.Fatalf("tracked assignment = (%q, %d), want (%q, %d)", got.beadID, got.assignmentID, "proposal-bead", 41)
	}
}

func sendWorkRequest(t *testing.T, d *Dispatcher, request protocol.Message) protocol.Message {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handleConn(context.Background(), server)
	}()

	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var response protocol.Message
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("short-lived work request connection did not close")
	}
	return response
}
