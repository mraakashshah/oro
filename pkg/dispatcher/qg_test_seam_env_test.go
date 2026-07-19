package dispatcher //nolint:testpackage // white-box: exercises unexported qgRunnerEnv

import (
	"strings"
	"testing"
)

// qgTestSeams are the TEST-ONLY environment seams that quality_gate.sh honors
// (marker mode, serial-lane-only, repo-root/cache overrides, sleeps, regression
// inject). If any of these leaked from the daemon environment into a spawned
// production gate, the gate could skip the entire main phase and pass with zero
// checks — so the env builder MUST strip them.
var qgTestSeams = []string{
	"ORO_QG_PHASE_MARKER_DIR",
	"ORO_QG_SERIAL_LANE_ONLY",
	"ORO_QG_SERIAL_LANE_RUN_OVERRIDE",
	"ORO_QG_REPO_ROOT_OVERRIDE",
	"ORO_QG_MAIN_SLEEP",
	"ORO_QG_SERIAL_SLEEP",
	"ORO_QG_PROBE_ID",
	"ORO_QG_INJECT_TIMING_REGRESSION",
}

func TestQGRunnerEnvStripsTestSeams(t *testing.T) {
	for _, seam := range qgTestSeams {
		t.Setenv(seam, "leaked")
	}
	env := qgRunnerEnv(false, t.TempDir(), "")
	for _, kv := range env {
		for _, seam := range qgTestSeams {
			if strings.HasPrefix(kv, seam+"=") {
				t.Errorf("qgRunnerEnv leaked test seam %q into a production gate env", seam)
			}
		}
	}
}
