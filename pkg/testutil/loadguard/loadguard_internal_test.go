package loadguard

import "testing"

type skipIfLoadedRecorder struct {
	testing.TB
	skipped bool
}

func (s *skipIfLoadedRecorder) Helper() {}

func (s *skipIfLoadedRecorder) Skip(...any) {
	s.skipped = true
}

func (s *skipIfLoadedRecorder) Skipf(string, ...any) {
	s.skipped = true
}

func skipIfLoadedSkipped(t *testing.T, fn func(testing.TB)) bool {
	t.Helper()
	recorder := &skipIfLoadedRecorder{TB: t}
	fn(recorder)
	return recorder.skipped
}

func TestSkipIfLoaded(t *testing.T) {
	t.Run("threshold", func(t *testing.T) {
		if !shouldSkip(12, 4, 1.5) {
			t.Fatal("expected load at threshold to skip")
		}
		if shouldSkip(5, 4, 1.5) {
			t.Fatal("expected load below threshold to run")
		}
	})

	t.Run("invalid inputs do not skip", func(t *testing.T) {
		if shouldSkip(12, 0, 1.5) {
			t.Fatal("zero CPUs should not skip")
		}
		if shouldSkip(12, 4, 0) {
			t.Fatal("zero threshold should not skip")
		}
	})

	t.Run("disable override runs", func(t *testing.T) {
		t.Setenv("ORO_LOADGUARD_DISABLE", "1")
		if skipIfLoadedSkipped(t, SkipIfLoaded) {
			t.Fatal("expected ORO_LOADGUARD_DISABLE=1 to prevent skip")
		}
	})

	t.Run("force override skips", func(t *testing.T) {
		t.Setenv("ORO_LOADGUARD_FORCE_SKIP", "1")
		if !skipIfLoadedSkipped(t, SkipIfLoaded) {
			t.Fatal("expected ORO_LOADGUARD_FORCE_SKIP=1 to skip")
		}
	})

	t.Run("one minute load probe", func(t *testing.T) {
		load, ok := oneMinuteLoad()
		if ok && load < 0 {
			t.Fatalf("oneMinuteLoad() = %f, want non-negative load", load)
		}
	})
}

func TestParseLoadAverage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  float64
		ok    bool
	}{
		{name: "plain value", input: "1.25 0.50 0.25", want: 1.25, ok: true},
		{name: "sysctl tuple", input: "{ 2.50 1.00 0.75 }", want: 2.5, ok: true},
		{name: "empty", input: "", ok: false},
		{name: "invalid", input: "not-a-number", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLoadAverage(tc.input)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("parseLoadAverage(%q) = (%v, %v), want (%v, %v)", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}
