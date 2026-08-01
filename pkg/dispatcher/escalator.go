package dispatcher

import (
	"context"
)

// Escalator accepts escalation messages from dispatcher checks.
type Escalator interface {
	Escalate(ctx context.Context, msg string) error
}

// EscalationType and FormatEscalation are now in pkg/protocol/types.go.

// NoopEscalator records no side effects while satisfying the Escalator
// interface for managerless dispatcher starts.
type NoopEscalator struct{}

// Escalate accepts escalation messages without delivering them anywhere.
func (NoopEscalator) Escalate(context.Context, string) error {
	return nil
}
