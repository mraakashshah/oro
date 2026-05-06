package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestVerifyRetroactiveGate invokes scripts/verify-retroactive-gate.sh as a
// subprocess and asserts exit 0, verifying the §18.6 premortem gate flow
// end-to-end: epic creation → 6 children → gate eligible → work refused →
// premortem auto-spawned → verdict=proceed → gate satisfied → work accepted.
func TestVerifyRetroactiveGate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	root := projectRoot(t)
	script := filepath.Join(root, "scripts", "verify-retroactive-gate.sh")

	if _, err := os.Stat(script); err != nil {
		t.Fatalf("verify-retroactive-gate.sh not found at %s: %v", script, err)
	}

	cmd := exec.Command("/bin/bash", script) //nolint:gosec // integration test, path is derived from repo root
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify-retroactive-gate.sh failed: %v\n--- output ---\n%s", err, out)
	}
	t.Logf("verify-retroactive-gate.sh output:\n%s", out)
}

// projectRoot walks up from the current working directory to find the module
// root (the directory containing go.mod).
func projectRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod not found)")
		}
		dir = parent
	}
}
