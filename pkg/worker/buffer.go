// Package worker implements the Oro worker agent. It connects to a Dispatcher
// over a Unix domain socket, receives bead assignments, spawns claude -p
// subprocesses, monitors context usage, and sends lifecycle messages back.
package worker

import (
	"sync"

	"oro/pkg/protocol"
)

// MessageBuffer is a bounded FIFO buffer for protocol messages.
// When full, the oldest messages are evicted to make room for new ones.
type MessageBuffer struct {
	mu   sync.Mutex
	msgs []protocol.Message
	cap  int
}

// NewMessageBuffer creates a buffer with the given maximum capacity.
func NewMessageBuffer(capacity int) *MessageBuffer {
	return &MessageBuffer{
		msgs: make([]protocol.Message, 0, capacity),
		cap:  capacity,
	}
}

// Add appends a message to the buffer. If the buffer is full, the oldest
// message is evicted.
func (b *MessageBuffer) Add(msg protocol.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.msgs) >= b.cap {
		// Evict oldest
		copy(b.msgs, b.msgs[1:])
		b.msgs[len(b.msgs)-1] = msg
	} else {
		b.msgs = append(b.msgs, msg)
	}
}

// Drain returns all buffered messages and clears the buffer.
func (b *MessageBuffer) Drain() []protocol.Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.msgs) == 0 {
		return nil
	}
	out := make([]protocol.Message, len(b.msgs))
	copy(out, b.msgs)
	b.msgs = b.msgs[:0]
	return out
}

// Len returns the number of buffered messages.
func (b *MessageBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.msgs)
}
