package qgserial //nolint:testpackage // white-box: exercises unexported skipUnlessSerialLane

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRequireSerialSkipsUnlessEnvSet verifies the serial-lane gate: the guarded
// test is skipped unless ORO_QG_SERIAL_LANE names a truthy value, and the skip
// reason names the env var so operators know how to enable the lane.
func TestRequireSerialSkipsUnlessEnvSet(t *testing.T) {
	t.Run("skips when unset", func(t *testing.T) {
		t.Setenv("ORO_QG_SERIAL_LANE", "")
		rec := runGuard(t)
		if !rec.skipped {
			t.Fatal("expected unset ORO_QG_SERIAL_LANE to skip")
		}
		if rec.helpers != 1 {
			t.Fatalf("serial guard must mark its helper frame once, got %d", rec.helpers)
		}
		if !strings.Contains(rec.reason, "ORO_QG_SERIAL_LANE") {
			t.Fatalf("skip reason must name the env var, got %q", rec.reason)
		}
	})

	t.Run("skips for falsey values", func(t *testing.T) {
		for _, v := range []string{"0", "false", "FALSE", "no", "off", " ", "bogus"} {
			t.Run(v, func(t *testing.T) {
				t.Setenv("ORO_QG_SERIAL_LANE", v)
				if !runGuard(t).skipped {
					t.Fatalf("expected ORO_QG_SERIAL_LANE=%q to skip", v)
				}
			})
		}
	})

	t.Run("runs for truthy values", func(t *testing.T) {
		for _, v := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
			t.Run(v, func(t *testing.T) {
				t.Setenv("ORO_QG_SERIAL_LANE", v)
				if runGuard(t).skipped {
					t.Fatalf("expected ORO_QG_SERIAL_LANE=%q to run", v)
				}
			})
		}
	})
}

func TestRequireStressSkipsUnlessEnvSet(t *testing.T) {
	t.Run("skips when unset", func(t *testing.T) {
		t.Setenv("ORO_QG_STRESS_LANE", "")
		rec := &skipRecorder{TB: t}
		skipUnlessStressLane(rec)
		if !rec.skipped {
			t.Fatal("expected unset ORO_QG_STRESS_LANE to skip")
		}
		if rec.helpers != 1 {
			t.Fatalf("stress guard must mark its helper frame once, got %d", rec.helpers)
		}
		if !strings.Contains(rec.reason, "ORO_QG_STRESS_LANE") {
			t.Fatalf("skip reason must name the env var, got %q", rec.reason)
		}
	})

	t.Run("runs only for truthy values", func(t *testing.T) {
		for _, tc := range []struct {
			value string
			runs  bool
		}{
			{value: "0"},
			{value: "false"},
			{value: "bogus"},
			{value: "1", runs: true},
			{value: "true", runs: true},
		} {
			t.Run(tc.value, func(t *testing.T) {
				t.Setenv("ORO_QG_STRESS_LANE", tc.value)
				rec := &skipRecorder{TB: t}
				skipUnlessStressLane(rec)
				if rec.skipped == tc.runs {
					t.Fatalf("ORO_QG_STRESS_LANE=%q skipped=%v, want runs=%v", tc.value, rec.skipped, tc.runs)
				}
			})
		}
	})
}

func TestRequireStressEntryPointSkipsOutsideLane(t *testing.T) {
	const helperEnv = "ORO_QG_REQUIRE_STRESS_HELPER"
	if os.Getenv(helperEnv) == "1" {
		t.Setenv(StressLaneEnvVar, "0")
		RequireStress(t)
		t.Fatal("RequireStress returned outside the stress lane")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRequireStressEntryPointSkipsOutsideLane$", "-test.v")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guarded helper process must skip cleanly: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "--- SKIP: TestRequireStressEntryPointSkipsOutsideLane") {
		t.Fatalf("guarded helper process did not report a skip:\n%s", output)
	}
}

// runGuard invokes the serial-lane skip helper against a recorder so the test
// can observe skip/run without skipping itself.
func runGuard(t *testing.T) *skipRecorder {
	t.Helper()
	rec := &skipRecorder{TB: t}
	skipUnlessSerialLane(rec)
	return rec
}

type skipRecorder struct {
	testing.TB
	skipped bool
	reason  string
	helpers int
}

func (s *skipRecorder) Helper() { s.helpers++ }

func (s *skipRecorder) Skip(args ...any) {
	s.skipped = true
	s.reason = fmt.Sprint(args...)
}

func (s *skipRecorder) Skipf(format string, args ...any) {
	s.skipped = true
	s.reason = fmt.Sprintf(format, args...)
}
