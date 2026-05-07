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

func TestShouldSkipOutsidePushQG(t *testing.T) {
	t.Run("skips when no QG context", func(t *testing.T) {
		if !shouldSkipOutsidePushQG("", "") {
			t.Fatal("expected skip when QG context is absent")
		}
	})
	t.Run("does not skip when context is push", func(t *testing.T) {
		if shouldSkipOutsidePushQG("push", "") {
			t.Fatal("expected no skip when context is push")
		}
	})
	t.Run("does not skip when context is pre-push", func(t *testing.T) {
		if shouldSkipOutsidePushQG("pre-push", "") {
			t.Fatal("expected no skip when context is pre-push")
		}
	})
	t.Run("does not skip when loadguard is disabled", func(t *testing.T) {
		if shouldSkipOutsidePushQG("", "1") {
			t.Fatal("expected no skip when ORO_LOADGUARD_DISABLE=1")
		}
	})
	t.Run("skips with non-push context even if other env set", func(t *testing.T) {
		if !shouldSkipOutsidePushQG("local", "") {
			t.Fatal("expected skip when context is local")
		}
	})
}
