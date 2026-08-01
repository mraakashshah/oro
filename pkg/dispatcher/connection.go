package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"net"

	"oro/pkg/protocol"
)

// acceptLoop accepts new worker connections.
func (d *Dispatcher) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		// Acquire semaphore slot before spawning handler
		select {
		case d.acceptSem <- struct{}{}:
			d.safeGo(func() {
				defer func() { <-d.acceptSem }() // Release semaphore slot
				d.handleConn(ctx, conn)
			})
		case <-ctx.Done():
			_ = conn.Close()
			return
		}
	}
}

func (d *Dispatcher) handleStatus(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Status == nil {
		return
	}
	d.touchProgress(workerID)
	evType := "status"
	if msg.Status.State == "qg_retry_received" {
		evType = "qg_retry_received"
	}
	payload := fmt.Sprintf(`{"state":%q,"result":%q}`, msg.Status.State, msg.Status.Result)
	if evType == "qg_retry_received" {
		_ = d.logEvent(ctx, evType, workerID, msg.Status.BeadID, workerID, payload)
		return
	}
	d.broadcastEvent(evType, msg.Status.BeadID, workerID)
}
