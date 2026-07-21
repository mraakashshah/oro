package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"oro/pkg/protocol"
)

// handleWorkRequestConn handles evidence and work-proposal requests on a
// short-lived connection. Returning true prevents handleConn from treating the
// request identity as a worker registration.
func (d *Dispatcher) handleWorkRequestConn(ctx context.Context, conn net.Conn, msg protocol.Message) bool {
	switch msg.Type {
	case protocol.MsgEvidenceRequest:
		response := d.handleEvidenceRequest(ctx, msg.EvidenceRequest)
		writeWorkRequestResponse(conn, protocol.Message{
			Type:             protocol.MsgEvidenceResponse,
			EvidenceResponse: &response,
		})
		return true
	case protocol.MsgWorkProposalRequest:
		response := protocol.WorkProposalResponse{}
		if msg.WorkProposalRequest == nil {
			response.Error = "missing work proposal request"
		} else {
			result, err := d.storeWorkProposal(ctx, msg.WorkProposalRequest.Proposal)
			if err != nil {
				response.Error = err.Error()
			} else {
				response.Result = result
			}
		}
		writeWorkRequestResponse(conn, protocol.Message{
			Type:                 protocol.MsgWorkProposalResponse,
			WorkProposalResponse: &response,
		})
		return true
	default:
		return false
	}
}

func (d *Dispatcher) handleEvidenceRequest(ctx context.Context, request *protocol.EvidenceRequest) protocol.EvidenceResponse {
	if request == nil {
		return protocol.EvidenceResponse{Error: "missing evidence request"}
	}
	if request.Execution == nil {
		return d.storeEvidenceRequest(ctx, request.Evidence)
	}
	return d.executeEvidenceRequest(ctx, request.Execution)
}

func (d *Dispatcher) storeEvidenceRequest(ctx context.Context, run protocol.EvidenceRun) protocol.EvidenceResponse {
	if err := d.storeEvidenceRun(ctx, run); err != nil {
		return protocol.EvidenceResponse{Error: err.Error()}
	}
	return protocol.EvidenceResponse{}
}

func (d *Dispatcher) executeEvidenceRequest(ctx context.Context, execution *protocol.EvidenceExecutionRequest) protocol.EvidenceResponse {
	manifest, err := d.RunEvidence(ctx, EvidenceRunRequest{
		AssignmentID: execution.AssignmentID,
		WorkerID:     execution.WorkerID,
		BeadID:       execution.BeadID,
		Argv:         execution.Argv,
		Timeout:      time.Duration(execution.TimeoutMS) * time.Millisecond,
	})
	if err != nil {
		return protocol.EvidenceResponse{Error: err.Error()}
	}
	return protocol.EvidenceResponse{Result: protocol.EvidenceRunResult{ID: manifest.ID, Status: string(manifest.Status), ExitCode: manifest.ExitCode}}
}

func (d *Dispatcher) storeEvidenceRun(ctx context.Context, run protocol.EvidenceRun) error {
	if d == nil || d.db == nil {
		return errors.New("store evidence run: dispatcher database is unavailable")
	}
	store, err := protocol.NewWorkProposalStore(d.db)
	if err != nil {
		return fmt.Errorf("create work proposal store: %w", err)
	}
	if err := store.StoreEvidenceRun(ctx, run); err != nil {
		return fmt.Errorf("store evidence run: %w", err)
	}
	return nil
}

func writeWorkRequestResponse(conn net.Conn, message protocol.Message) {
	_ = json.NewEncoder(conn).Encode(message)
}
