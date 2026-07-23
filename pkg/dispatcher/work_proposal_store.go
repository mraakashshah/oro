package dispatcher

import (
	"context"
	"errors"
	"fmt"

	"oro/pkg/protocol"
)

// WorkProposalPayload is the dispatcher-facing alias for a provisional work
// proposal. Canonical scope derivation remains a later controller concern.
type WorkProposalPayload = protocol.WorkProposalPayload

// WorkProposalResult is the durable response returned to a proposal producer.
type WorkProposalResult = protocol.WorkProposalResult

// storeWorkProposal persists a provisional proposal and provides exact replay
// semantics for an assignment/client proposal ID pair.
func (d *Dispatcher) storeWorkProposal(ctx context.Context, payload WorkProposalPayload) (WorkProposalResult, error) {
	if d == nil || d.db == nil {
		return WorkProposalResult{}, errors.New("store work proposal: dispatcher database is unavailable")
	}
	store, err := protocol.NewWorkProposalStore(d.db)
	if err != nil {
		return WorkProposalResult{}, fmt.Errorf("create work proposal store: %w", err)
	}
	result, err := store.StoreWorkProposal(ctx, payload)
	if err != nil {
		return WorkProposalResult{}, fmt.Errorf("persist work proposal: %w", err)
	}
	return result, nil
}
