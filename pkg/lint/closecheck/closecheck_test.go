package closecheck_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"oro/pkg/lint/closecheck"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "closecheck_fixture")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// pkg/lint/closecheck → go up 3 levels
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// TestFlagsNonTestStoreCloseCalls verifies the core detection rules:
//   - violation.go (beads.Close / store.Close): flagged with file:line
//   - violation_test.go (_test.go): NOT flagged
//   - migration.go (//go:build migration): NOT flagged
//   - clean.go (d.CloseBead / CloseBead body): NOT flagged
func TestFlagsNonTestStoreCloseCalls(t *testing.T) {
	findings, err := closecheck.CheckDir(fixtureDir(t))
	if err != nil {
		t.Fatalf("CheckDir: %v", err)
	}

	byFile := map[string][]closecheck.Finding{}
	for _, f := range findings {
		base := filepath.Base(f.File)
		byFile[base] = append(byFile[base], f)
	}

	if len(byFile["violation.go"]) == 0 {
		t.Error("expected findings in violation.go, got none")
	}
	for _, f := range byFile["violation.go"] {
		if f.File == "" || f.Line == 0 {
			t.Errorf("finding missing file:line: %+v", f)
		}
	}

	if len(byFile["violation_test.go"]) > 0 {
		t.Errorf("violation_test.go must not be flagged, got %d findings", len(byFile["violation_test.go"]))
	}
	if len(byFile["migration.go"]) > 0 {
		t.Errorf("migration.go must not be flagged, got %d findings", len(byFile["migration.go"]))
	}
	if len(byFile["clean.go"]) > 0 {
		t.Errorf("clean.go must not be flagged, got %d findings: %v", len(byFile["clean.go"]), byFile["clean.go"])
	}
}

// TestPassesOnCleanTree runs the checker against pkg/dispatcher/ after migration
// and asserts zero findings — the 3 direct d.beads.Close callsites must be gone.
func TestPassesOnCleanTree(t *testing.T) {
	dispatcherDir := filepath.Join(repoRoot(t), "pkg", "dispatcher")

	findings, err := closecheck.CheckDir(dispatcherDir)
	if err != nil {
		t.Fatalf("CheckDir(pkg/dispatcher): %v", err)
	}

	for _, f := range findings {
		t.Errorf("unexpected finding in pkg/dispatcher: %s:%d: %s", f.File, f.Line, f.Text)
	}
}

// TestCLIExitCode verifies scripts/closecheck.sh exits 0 on clean and non-zero
// when pointed at a directory containing violations.
func TestCLIExitCode(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "closecheck.sh")

	t.Run("clean_exits_zero", func(t *testing.T) {
		cmd := exec.Command("bash", script, filepath.Join(root, "pkg", "dispatcher"))
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("expected exit 0 for clean tree, got: %v\noutput: %s", err, out)
		}
	})

	t.Run("violation_exits_nonzero", func(t *testing.T) {
		cmd := exec.Command("bash", script, fixtureDir(t))
		cmd.Dir = root
		if err := cmd.Run(); err == nil {
			t.Error("expected non-zero exit for fixture violations, got exit 0")
		}
	})
}
