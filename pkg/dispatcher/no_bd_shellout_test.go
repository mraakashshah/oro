package dispatcher_test

import (
	"os"
	"strings"
	"testing"
)

// TestNoBdCreateJSONShellOut guards against re-introducing the legacy bd create --json
// shell-out that contaminated JSON with stderr warnings. bd v1.0.2 writes
// "Warning: auto-export: git add failed" to stderr; when collected alongside stdout
// via CombinedOutput, json.Unmarshal fails at the leading 'W'.
//
// Fix (oro-37t5): deleted beadsource.go (CLIStore) entirely. The dispatcher now
// uses beadstore.SQLiteStore via the DeferredStore interface; no bd shell-out occurs.
// ExecCommandRunner.Run uses cmd.Output() (stdout only) so stderr warnings never
// reach JSON parsers even if a CLI-backed path is temporarily re-introduced.
func TestNoBdCreateJSONShellOut(t *testing.T) {
	// beadsource.go contained CLIStore.Create() which ran `bd create --json`.
	// Ensure it stays deleted — re-adding it re-introduces the contamination bug.
	if _, err := os.Stat("beadsource.go"); err == nil {
		t.Fatal("beadsource.go must stay deleted: it contained CLIStore.Create() " +
			"which shelled out to bd create --json; stderr warnings (e.g. " +
			"\"Warning: auto-export: git add failed\") contaminate JSON output. " +
			"Use beadstore.SQLiteStore via DeferredStore instead.")
	}

	// ExecCommandRunner.Run must use cmd.Output() (stdout-only), never
	// cmd.CombinedOutput(), so JSON output is not polluted by stderr warnings.
	src, err := os.ReadFile("exec_runner.go")
	if err != nil {
		t.Fatalf("read exec_runner.go: %v", err)
	}
	if strings.Contains(string(src), "CombinedOutput") {
		t.Error("exec_runner.go: ExecCommandRunner.Run must use cmd.Output(), " +
			"not CombinedOutput() — stderr noise contaminates JSON when bd (or any " +
			"CLI tool) writes warnings at runtime")
	}
}
