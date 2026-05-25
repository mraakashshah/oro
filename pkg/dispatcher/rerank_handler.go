package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"oro/pkg/protocol"
)

// ErrRerankerUnavailable is returned by lazyLoadReranker when the reranker
// model could not be loaded (factory nil, model missing, or prior load failure).
var ErrRerankerUnavailable = errors.New("reranker unavailable")

// lazyLoadReranker loads the BGE reranker via rerankerFactory on the first call,
// caches the result (success or failure) in sync.Once, and returns it on every
// subsequent call without re-invoking the factory.
func (d *Dispatcher) lazyLoadReranker() (Reranker, error) {
	d.rerankerOnce.Do(func() {
		if d.rerankerFactory == nil {
			d.rerankerErr = ErrRerankerUnavailable
			return
		}
		r, err := d.rerankerFactory(d.cfg.RerankerModelDir)
		if err != nil {
			d.rerankerErr = ErrRerankerUnavailable
			return
		}
		d.reranker = r
	})
	if d.rerankerErr != nil {
		return nil, d.rerankerErr
	}
	return d.reranker, nil
}

// handleRerankByIDs resolves each MemoryID from the dispatcher's own Store
// (project-unscoped), calls the reranker, and returns scores in input order.
// IDs not found in the Store are represented as empty strings; the reranker
// still runs over all positions so the caller's score-to-ID mapping is preserved.
func (d *Dispatcher) handleRerankByIDs(ctx context.Context, req protocol.RerankByIDsRequest) protocol.RerankByIDsResponse {
	r, err := d.lazyLoadReranker()
	if err != nil {
		return protocol.RerankByIDsResponse{Err: "reranker unavailable"}
	}

	docs := make([]string, len(req.MemoryIDs))
	for i, id := range req.MemoryIDs {
		m, getErr := d.memories.GetByID(ctx, id)
		if getErr == nil {
			docs[i] = m.Content
		}
		// on error: docs[i] stays "" — reranker still runs for this position
	}

	return protocol.RerankByIDsResponse{Scores: r.Rerank(req.Query, docs)}
}

// handleRerankByIDsWithResponse handles a MsgRerankByIDsRequest on a short-lived
// UDS connection: it runs handleRerankByIDs and writes the response before returning.
func (d *Dispatcher) handleRerankByIDsWithResponse(ctx context.Context, conn net.Conn, msg protocol.Message) {
	if msg.RerankReq == nil {
		return
	}
	resp := d.handleRerankByIDs(ctx, *msg.RerankReq)
	respMsg := protocol.Message{
		Type:       protocol.MsgRerankByIDsResponse,
		RerankResp: &resp,
	}
	data, err := json.Marshal(respMsg)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}
