// Package web provides HTTP and Server-Sent Events (SSE) functionality.
package web

import (
	"encoding/json"
	"fmt"
	"sync"
)

// SSEBroadcaster broadcasts Server-Sent Events to connected clients.
// It is safe for concurrent use.
type SSEBroadcaster interface {
	Send(eventType, beadID, workerID string)
	// Subscribe registers a new client and returns a buffered channel that will
	// receive formatted SSE messages. The caller must call Unsubscribe when done.
	Subscribe() chan string
	// Unsubscribe removes the channel registered by a prior Subscribe call.
	Unsubscribe(ch chan string)
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

// Subscribe registers a new client and returns a buffered channel that will
// receive formatted SSE messages. The channel pointer is used as the map key.
func (b *sseImpl) Subscribe() chan string {
	ch := make(chan string, 16)
	key := fmt.Sprintf("%p", ch)
	b.mu.Lock()
	b.clients[key] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel registered by a prior Subscribe call.
func (b *sseImpl) Unsubscribe(ch chan string) {
	key := fmt.Sprintf("%p", ch)
	b.mu.Lock()
	delete(b.clients, key)
	b.mu.Unlock()
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
