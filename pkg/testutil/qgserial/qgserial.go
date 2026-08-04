// Package qgserial gates concurrency-flaky tests into a serialized quality-gate
// lane.
//
// Three orthogonal test guards exist in this codebase. Pick the right one:
//
//   - qgserial.RequireSerial (this package, env ORO_QG_SERIAL_LANE) — use for a
//     test that opens a REAL Unix-domain-socket/net listener or asserts tight
//     wall-clock timing bounds. This is the class that flakes when many worker
//     quality gates run concurrently and contend for CPU: the dispatcher's 2s
//     listener wait and socket read/write deadlines miss under load. Guarded
//     tests self-skip in the concurrent main gate and run only in the dedicated
//     serial lane, which the quality gate runs under its cross-worktree FIFO lock
//     (see scripts/quality_gate.sh). The canonical guarded set lives in
//     pkg/dispatcher/testdata/serial_lane_tests.txt.
//
//   - qgserial.RequireStress (this package, env ORO_QG_STRESS_LANE) — use for
//     an integration test that launches nested quality gates or another
//     resource-intensive test topology. These tests skip ordinary package and
//     coverage runs and execute only in an explicit, cache-isolated CI lane.
//
//   - loadguard.SkipOutsidePushQG (pkg/testutil/loadguard, env ORO_QG_CONTEXT) —
//     use for a timing-sensitive test that should run ONLY in the push/pre-push
//     gate, not in local dev gates. That axis is "which gate context", not
//     "concurrency-flaky". A test can want neither, one, or (rarely) both.
package qgserial

import (
	"os"
	"strings"
	"testing"
)

// SerialLaneEnvVar is the environment variable that opts a test run into the
// serialized quality-gate lane. It is set to a truthy value only by the serial
// lane in scripts/quality_gate.sh; the concurrent main phase actively neutralizes
// it so guarded tests stay skipped there.
const SerialLaneEnvVar = "ORO_QG_SERIAL_LANE"

// StressLaneEnvVar opts resource-intensive nested quality-gate integration
// tests into their explicit CI lane.
const StressLaneEnvVar = "ORO_QG_STRESS_LANE"

// RequireSerial skips the calling test unless the serial lane is enabled via a
// truthy ORO_QG_SERIAL_LANE. Call it as the first line of any test that stands up
// a real socket listener or asserts tight wall-clock timing, so the concurrent
// main gate skips it and only the serialized lane runs it.
//
//oro:testonly
func RequireSerial(t *testing.T) {
	t.Helper()
	skipUnlessSerialLane(t)
}

// skipUnlessSerialLane is the testing.TB-typed core so the behavior is unit
// testable against a recorder (RequireSerial is the *testing.T call site).
func skipUnlessSerialLane(tb testing.TB) {
	tb.Helper()
	if !SerialLaneEnabled() {
		tb.Skipf("skipping concurrency-flaky test outside the serial lane; set %s=1 to run", SerialLaneEnvVar)
	}
}

// RequireStress skips the calling test unless the explicit stress lane is
// enabled. Ordinary package, coverage, and CGO-free runs must leave it false.
//
//oro:testonly
func RequireStress(t *testing.T) {
	t.Helper()
	skipUnlessStressLane(t)
}

func skipUnlessStressLane(tb testing.TB) {
	tb.Helper()
	if !StressLaneEnabled() {
		tb.Skipf("skipping resource-intensive test outside the stress lane; set %s=1 to run", StressLaneEnvVar)
	}
}

// SerialLaneEnabled reports whether the serial lane is enabled in the current
// environment.
func SerialLaneEnabled() bool {
	return truthy(os.Getenv(SerialLaneEnvVar))
}

// StressLaneEnabled reports whether the resource-intensive stress lane is
// enabled in the current environment.
func StressLaneEnabled() bool {
	return truthy(os.Getenv(StressLaneEnvVar))
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
