package fixture

import (
	"context"
	"testing"
)

// TestCloseInTest: store.Close in a _test.go file — must NOT be flagged.
func TestCloseInTest(t *testing.T) {
	t.Helper()
	var store Store
	_ = store.Close(context.Background(), "id", "reason")
}
