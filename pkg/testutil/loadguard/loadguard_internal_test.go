package loadguard

import "testing"

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
}
