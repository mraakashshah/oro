package qgserial

import (
	"fmt"
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
}

func (s *skipRecorder) Helper() {}

func (s *skipRecorder) Skip(args ...any) {
	s.skipped = true
	s.reason = fmt.Sprint(args...)
}

func (s *skipRecorder) Skipf(format string, args ...any) {
	s.skipped = true
	s.reason = fmt.Sprintf(format, args...)
}
