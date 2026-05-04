package dispatcher_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/dispatcher"
)

// TestCLIBeadSource_UpdateInProgressPersists guards against stderr warnings from
// external CLI tools (e.g. bd v1.0.2 "Warning: auto-export: git add failed")
// contaminating JSON output and causing json.Unmarshal failures.
//
// Before oro-37t5, CLIStore.Update used CombinedOutput; the warning character
// 'W' was prepended to stdout JSON and json.Unmarshal failed with:
//
//	invalid character 'W' looking for beginning of value
//
// ExecCommandRunner.Run uses cmd.Output() (stdout-only), so stderr warnings
// never reach JSON parsers.
func TestCLIBeadSource_UpdateInProgressPersists(t *testing.T) {
	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}

	// Fake oro binary: writes "Warning: auto-export: git add failed" to stderr
	// on every call (simulating bd v1.0.2 behaviour) and returns appropriate
	// stdout for update and show sub-commands.
	fakeOro := filepath.Join(binDir, "oro")
	script := `#!/bin/sh
echo 'Warning: auto-export: git add failed' >&2
if [ "$1" = "bead" ] && [ "$2" = "update" ]; then
  exit 0
fi
if [ "$1" = "bead" ] && [ "$2" = "show" ]; then
  printf '[{"id":"oro-test","title":"T","status":"in_progress"}]'
  exit 0
fi
echo "unexpected: $*" >&2
exit 1
`
	if err := os.WriteFile(fakeOro, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake oro: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &dispatcher.ExecCommandRunner{Dir: tmpDir}
	ctx := context.Background()

	// Update must succeed even though the fake oro writes a warning to stderr.
	_, err := runner.Run(ctx, "oro", "bead", "update", "oro-test", "--status=in_progress")
	if err != nil {
		t.Fatalf("Run bead update: %v (stderr warning must not cause failure)", err)
	}

	// Show output must be parseable JSON — the stderr warning must NOT appear in out.
	out, err := runner.Run(ctx, "oro", "bead", "show", "oro-test", "--json")
	if err != nil {
		t.Fatalf("Run bead show: %v", err)
	}

	var beads []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &beads); err != nil {
		t.Fatalf("json.Unmarshal bead show output: %v\nraw output: %q\n"+
			"(stderr warning must not leak into stdout)", err, out)
	}
	if len(beads) == 0 || beads[0].Status != "in_progress" {
		t.Errorf("status after update: got %v, want in_progress", beads)
	}
}
