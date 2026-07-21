package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"oro/pkg/protocol"
)

// handleWorkRequestConn handles evidence and work-proposal requests on a
// short-lived connection. Returning true prevents handleConn from treating the
// request identity as a worker registration.
func (d *Dispatcher) handleWorkRequestConn(ctx context.Context, conn net.Conn, msg protocol.Message) bool {
	switch msg.Type {
	case protocol.MsgEvidenceRequest:
		response := protocol.EvidenceResponse{}
		if msg.EvidenceRequest == nil {
			response.Error = "missing evidence request"
		} else if err := d.storeEvidenceRun(ctx, msg.EvidenceRequest.Evidence); err != nil {
			response.Error = err.Error()
		}
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
