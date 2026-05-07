package loadguard_test

import (
	"testing"

	"oro/pkg/testutil/loadguard"
)

func TestSkipOutsidePushQG(t *testing.T) {
	t.Run("skips without qg context", func(t *testing.T) {
		t.Setenv("ORO_QG_CONTEXT", "")
		skipped := helperSkipped(t, func(tb testing.TB) {
			tb.Helper()
			loadguard.SkipOutsidePushQG(tb)
		})
		if !skipped {
			t.Fatal("expected unset ORO_QG_CONTEXT to skip")
		}
	})

	t.Run("skips local qg context", func(t *testing.T) {
		t.Setenv("ORO_QG_CONTEXT", "local")
		skipped := helperSkipped(t, func(tb testing.TB) {
			tb.Helper()
			loadguard.SkipOutsidePushQG(tb)
		})
		if !skipped {
			t.Fatal("expected local ORO_QG_CONTEXT to skip")
		}
	})

	t.Run("runs in push contexts", func(t *testing.T) {
		for _, context := range []string{"push", "pre-push"} {
			t.Run(context, func(t *testing.T) {
				t.Setenv("ORO_QG_CONTEXT", context)
				skipped := helperSkipped(t, func(tb testing.TB) {
					tb.Helper()
					loadguard.SkipOutsidePushQG(tb)
				})
				if skipped {
					t.Fatalf("expected %s ORO_QG_CONTEXT to run", context)
				}
			})
		}
	})

	t.Run("disable override runs", func(t *testing.T) {
		t.Setenv("ORO_QG_CONTEXT", "local")
		t.Setenv("ORO_LOADGUARD_DISABLE", "1")
		skipped := helperSkipped(t, func(tb testing.TB) {
			tb.Helper()
			loadguard.SkipOutsidePushQG(tb)
		})
		if skipped {
			t.Fatal("expected ORO_LOADGUARD_DISABLE=1 to prevent skip")
		}
	})
}

type skipRecorder struct {
	testing.TB
	skipped bool
}

func (s *skipRecorder) Helper() {}

func (s *skipRecorder) Skip(args ...any) {
	s.skipped = true
}

func (s *skipRecorder) Skipf(format string, args ...any) {
	s.skipped = true
}

func helperSkipped(t *testing.T, fn func(testing.TB)) bool {
	t.Helper()
	recorder := &skipRecorder{TB: t}
	fn(recorder)
	return recorder.skipped
}
