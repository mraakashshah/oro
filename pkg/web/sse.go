// Package web provides HTTP and Server-Sent Events (SSE) functionality.
package web

import (
	"encoding/json"
	"sync"
)

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
	event := formatSSEEvent(eventType, beadID, workerID)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

// sseEvent is the JSON payload for a server-sent event.
type sseEvent struct {
	Type     string `json:"type"`
	BeadID   string `json:"bead_id"`
	WorkerID string `json:"worker_id"`
}

// formatSSEEvent formats an event as Server-Sent Event data.
func formatSSEEvent(eventType, beadID, workerID string) string {
	b, _ := json.Marshal(sseEvent{Type: eventType, BeadID: beadID, WorkerID: workerID})
	return "data: " + string(b) + "\n\n"
}
