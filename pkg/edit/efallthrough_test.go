package edit_test

import (
	"errors"
	"testing"

	"oro/pkg/edit"
)

func TestEFallthrough(t *testing.T) {
	t.Run("reason text matches §7.6 for 1-anchor ineligible snippet", func(t *testing.T) {
		orig := []string{"a := 1", "b := 2"}
		snippet := []string{"a := 1", "new line"} // only 1 anchor

		_, err := edit.Splice(orig, snippet, "// ...")

		var fe *edit.FallthroughError
		if !errors.As(err, &fe) {
			t.Fatalf("expected *edit.FallthroughError, got %T: %v", err, err)
		}

		const wantMsg = "oro edit:replace failed: SPLICE_INELIGIBLE\n" +
			"Reason: only 1 anchor line matched; need at least 2.\n" +
			"Recommendation: use Edit tool with full block."

		if got := fe.WorkerMessage(); got != wantMsg {
			t.Fatalf("WorkerMessage():\ngot:  %q\nwant: %q", got, wantMsg)
		}
	})

	t.Run("FallthroughError still satisfies errors.Is(err, ErrFallthrough)", func(t *testing.T) {
		orig := []string{"a := 1", "b := 2"}
		snippet := []string{"a := 1", "new line"} // 1 anchor only

		_, err := edit.Splice(orig, snippet, "// ...")
		if !errors.Is(err, edit.ErrFallthrough) {
			t.Fatalf("expected errors.Is(err, ErrFallthrough)=true, got err=%v", err)
		}
	})

	t.Run("native Edit completes work when splice is ineligible", func(t *testing.T) {
		snippet := []string{"brand new line 1", "brand new line 2"}

		got := edit.NativeEdit(snippet)
		if !slicesEqual(got, snippet) {
			t.Fatalf("NativeEdit() = %v, want %v", got, snippet)
		}
	})

	t.Run("bead succeeds: SpliceOrNative falls back to native Edit", func(t *testing.T) {
		orig := []string{"a := 1", "b := 2"}
		snippet := []string{"a := 1", "new line"} // 1 anchor → ineligible

		result, usedFallback, err := edit.SpliceOrNative(orig, snippet, "// ...")
		if err != nil {
			t.Fatalf("SpliceOrNative() unexpected error: %v", err)
		}
		if !usedFallback {
			t.Fatal("expected usedFallback=true")
		}
		if !slicesEqual(result, snippet) {
			t.Fatalf("SpliceOrNative() result = %v, want %v", result, snippet)
		}
	})
}
