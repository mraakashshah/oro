package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

var workProposalCLISocketSequence atomic.Uint64

func TestEvidenceAndProposalCLIRoundTrip(t *testing.T) {
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_WORKER_ID", "worker-evidence-cli")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-evidence-cli")

	socketPath := filepath.Join("/tmp", fmt.Sprintf("oro-work-proposal-cli-%d-%d.sock", os.Getpid(), workProposalCLISocketSequence.Add(1)))
	t.Setenv("ORO_SOCKET_PATH", socketPath)
	credentialPath := filepath.Join(t.TempDir(), "assignment-capability.json")
	t.Setenv("ORO_CAPABILITY_FILE", credentialPath)
	writeWorkProposalCLICredential(t, credentialPath, 41, 1)

	requests, stop := startWorkProposalCLIServer(t, socketPath, 3)
	defer stop()

	evidenceOutput, _, err := executeCommand("evidence", "run", "--kind", "diagnostic", "--timeout", "2m", "--", "printf", "evidence")
	if err != nil {
		t.Fatalf("evidence run: %v", err)
	}
	var evidenceResult evidenceCLIResult
	if err := json.Unmarshal([]byte(evidenceOutput), &evidenceResult); err != nil {
		t.Fatalf("decode evidence output %q: %v", evidenceOutput, err)
	}
	if evidenceResult.ID == "" || evidenceResult.Status != "completed" {
		t.Fatalf("evidence result = %#v, want completed result with ID", evidenceResult)
	}

	proposalArgs := []string{
		"task", "propose-blocker",
		"--evidence-run", evidenceResult.ID,
		"--fingerprint", "missing-prerequisite",
		"--kind", "prerequisite",
		"--summary", "need the prerequisite task",
		"--client-id", "proposal-retry-key",
	}
	firstOutput, _, err := executeCommand(proposalArgs...)
	if err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	secondOutput, _, err := executeCommand(proposalArgs...)
	if err != nil {
		t.Fatalf("retry proposal: %v", err)
	}
	if firstOutput != secondOutput {
		t.Fatalf("retry output = %q, want exact replay %q", secondOutput, firstOutput)
	}

	got := make([]protocol.Message, 0, 3)
	for range 3 {
		select {
		case request := <-requests:
			got = append(got, request)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for work proposal request")
		}
	}
	if got[0].Type != protocol.MsgEvidenceRequest || got[0].EvidenceRequest == nil || got[0].EvidenceRequest.Execution == nil {
		t.Fatalf("first request = %#v, want typed evidence request", got[0])
	}
	evidence := got[0].EvidenceRequest.Execution
	if evidence.AssignmentID != 41 || evidence.WorkerID != "worker-evidence-cli" || evidence.BeadID != "oro-evidence-cli" || evidence.Kind != "diagnostic" || strings.Join(evidence.Argv, " ") != "printf evidence" || evidence.Capability.Token != "token-cli" || evidence.Capability.Nonce == "" {
		t.Fatalf("evidence request = %#v", evidence)
	}
	for index, request := range got[1:] {
		if request.Type != protocol.MsgWorkProposalRequest || request.WorkProposalRequest == nil {
			t.Fatalf("proposal request %d = %#v, want typed proposal request", index, request)
		}
		proposal := request.WorkProposalRequest.Proposal
		if proposal.AssignmentID != 41 || proposal.WorkerID != "worker-evidence-cli" || proposal.BeadID != "oro-evidence-cli" || proposal.EvidenceRunID != evidenceResult.ID || proposal.ClientProposalID != "proposal-retry-key" || proposal.Kind != "prerequisite" || request.WorkProposalRequest.Capability.Nonce == "" {
			t.Fatalf("proposal request %d = %#v", index, proposal)
		}
	}

	writeWorkProposalCLICredential(t, credentialPath, 0, 2)
	if _, _, err := executeCommand(proposalArgs...); err == nil || !strings.Contains(err.Error(), "assignment") {
		t.Fatalf("proposal with refreshed invalid credential error = %v, want assignment validation error", err)
	}

	t.Setenv("ORO_CAPABILITY_FILE", filepath.Join(t.TempDir(), "missing-capability.json"))
	if _, _, err := executeCommand(proposalArgs...); err == nil || !strings.Contains(err.Error(), "credential_unavailable") {
		t.Fatalf("proposal with missing credential error = %v, want fail-closed credential error", err)
	}

	writeWorkProposalCLICredential(t, credentialPath, 41, 3)
	t.Setenv("ORO_CAPABILITY_FILE", credentialPath)
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(t.TempDir(), "missing.sock"))
	if _, _, err := executeCommand(proposalArgs...); err == nil || !strings.Contains(err.Error(), "socket_unavailable") {
		t.Fatalf("proposal with missing socket error = %v, want fail-closed socket error", err)
	}

	timeoutSocket := filepath.Join("/tmp", fmt.Sprintf("oro-work-proposal-timeout-%d-%d.sock", os.Getpid(), workProposalCLISocketSequence.Add(1)))
	t.Setenv("ORO_SOCKET_PATH", timeoutSocket)
	stopTimeoutServer := startWorkProposalCLITimeoutServer(t, timeoutSocket)
	defer stopTimeoutServer()
	if _, _, err := executeCommand(append(proposalArgs, "--timeout", "5ms")...); err == nil || !strings.Contains(err.Error(), `"code":"timeout"`) {
		t.Fatalf("proposal timeout error = %v, want structured timeout error", err)
	}
}

type evidenceCLIResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func writeWorkProposalCLICredential(t *testing.T, path string, assignmentID, generation int64) {
	t.Helper()
	if err := worker.ReplaceCapabilityFile(path, worker.AssignmentCredential{
		AssignmentID: assignmentID,
		Generation:   generation,
		CapabilityID: "capability-cli",
		Token:        "token-cli",
		ExpiresAt:    time.Now().Add(time.Hour).UTC(),
	}); err != nil {
		t.Fatalf("write capability credential: %v", err)
	}
}

func startWorkProposalCLIServer(t *testing.T, socketPath string, requests int) (<-chan protocol.Message, func()) {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen %q: %v", socketPath, err)
	}
	received := make(chan protocol.Message, requests)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()
		for range requests {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request protocol.Message
			err = json.NewDecoder(conn).Decode(&request)
			if err == nil {
				received <- request
				response := workProposalCLIResponse(request)
				_ = json.NewEncoder(conn).Encode(response)
			}
			_ = conn.Close()
		}
		<-ctx.Done()
	}()
	return received, func() {
		cancel()
		_ = listener.Close()
		<-done
		_ = os.Remove(socketPath)
	}
}

func workProposalCLIResponse(request protocol.Message) protocol.Message {
	switch request.Type {
	case protocol.MsgEvidenceRequest:
		return protocol.Message{Type: protocol.MsgEvidenceResponse, EvidenceResponse: &protocol.EvidenceResponse{Result: protocol.EvidenceRunResult{ID: "evidence-stable", Status: "completed", ExitCode: 0}}}
	case protocol.MsgWorkProposalRequest:
		return protocol.Message{Type: protocol.MsgWorkProposalResponse, WorkProposalResponse: &protocol.WorkProposalResponse{Result: protocol.WorkProposalResult{ProposalID: "proposal-stable", State: "pending"}}}
	default:
		return protocol.Message{}
	}
}

func startWorkProposalCLITimeoutServer(t *testing.T, socketPath string) func() {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen timeout server %q: %v", socketPath, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request protocol.Message
		_ = json.NewDecoder(conn).Decode(&request)
		time.Sleep(50 * time.Millisecond)
	}()
	return func() {
		_ = listener.Close()
		<-done
		_ = os.Remove(socketPath)
	}
}
