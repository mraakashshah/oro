// Package web provides HTTP and Server-Sent Events (SSE) functionality.
package web

import "sync"

// SSEBroadcaster broadcasts Server-Sent Events to connected clients.
// It is safe for concurrent use.
type SSEBroadcaster interface {
	Send(eventType, beadID, workerID string)
}

// sseImpl is the concrete implementation of SSEBroadcaster.
type sseImpl struct {
	mu      sync.RWMutex
	clients map[string]chan string // client ID -> channel
}

// NewSSEBroadcaster creates a new SSEBroadcaster.
func NewSSEBroadcaster() SSEBroadcaster {
	return &sseImpl{
		clients: make(map[string]chan string),
	}
}

// Send broadcasts an event to all connected clients.
// It is safe for concurrent use.
func (b *sseImpl) Send(eventType, beadID, workerID string) {
	b.mu.RLock()
	clients := b.clients
	b.mu.RUnlock()

	// Format the event as SSE data
	event := formatSSEEvent(eventType, beadID, workerID)

	// Send to all clients
	for _, ch := range clients {
		select {
		case ch <- event:
		default:
			// Client channel full, skip (non-blocking)
		}
	}
}

// formatSSEEvent formats an event as Server-Sent Event data.
func formatSSEEvent(eventType, beadID, workerID string) string {
	return "data: {\"type\":\"" + eventType + "\",\"bead_id\":\"" + beadID + "\",\"worker_id\":\"" + workerID + "\"}\n\n"
}
