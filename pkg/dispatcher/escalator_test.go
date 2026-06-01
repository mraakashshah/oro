package dispatcher_test

import (
	"context"
	"testing"

	"oro/pkg/dispatcher"
)

func TestNoopEscalatorReturnsNil(t *testing.T) {
	t.Parallel()

	var esc dispatcher.Escalator = dispatcher.NoopEscalator{}
	for _, msg := range []string{"", "arbitrary escalation message"} {
		if err := esc.Escalate(context.Background(), msg); err != nil {
			t.Fatalf("NoopEscalator.Escalate(%q) returned error: %v", msg, err)
		}
	}
}
