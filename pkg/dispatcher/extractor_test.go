package dispatcher //nolint:testpackage // needs access to unexported extractAndStoreLearnings

import (
	"context"
	"testing"
)

func TestExtractAndStoreLearnings_NoOp(t *testing.T) {
	// Verify it doesn't panic and doesn't require db/memories.
	d := &Dispatcher{}
	d.extractAndStoreLearnings(context.Background(), "test-bead")
	// If we get here without panic, it's a no-op.
}
