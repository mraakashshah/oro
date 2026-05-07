package integration_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRetroactiveGateScriptRemoved(t *testing.T) {
	_, err := os.Stat(filepath.Join("..", "..", "scripts", "verify-retroactive-gate.sh"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verify-retroactive-gate.sh exists or stat failed with unexpected error: %v", err)
	}
}
