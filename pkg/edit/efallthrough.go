package edit

import (
	"errors"
	"fmt"
)

// FallthroughError is returned by Splice when the snippet is ineligible for
// anchor splice. It wraps ErrFallthrough so callers can use errors.Is for
// backward compatibility, and carries a human-readable Reason.
type FallthroughError struct {
	Reason string
}

func (e *FallthroughError) Error() string { return ErrFallthrough.Error() }

//oro:testonly — Unwrap is implicit contract for errors.Is; wired from tests only until CLI surface lands (Phase C.2)
func (e *FallthroughError) Unwrap() error { return ErrFallthrough }

// WorkerMessage returns the §7.6 message shown to the worker when EFALLTHROUGH
// occurs during an oro edit:replace operation.
//
//oro:testonly — wired from production by the pkg/edit CLI surface bead (Phase C.2)
func (e *FallthroughError) WorkerMessage() string {
	return fmt.Sprintf(
		"oro edit:replace failed: SPLICE_INELIGIBLE\nReason: %s\nRecommendation: use Edit tool with full block.",
		e.Reason,
	)
}

// NativeEdit returns snippet as the new body, bypassing anchor-splice logic.
// This is the fallback when Splice returns EFALLTHROUGH — equivalent to the
// worker reaching for the native Edit tool with the full replacement block.
func NativeEdit(snippet []string) []string {
	return snippet
}

// SpliceOrNative attempts anchor splice; if the snippet is ineligible
// (EFALLTHROUGH), it falls back to NativeEdit. usedFallback is true iff
// NativeEdit was used. err is non-nil only for unexpected failures.
//
//oro:testonly — wired from production by the pkg/edit CLI surface bead (Phase C.2)
func SpliceOrNative(orig, snippet []string, contMarker string) (body []string, usedFallback bool, err error) {
	result, spliceErr := Splice(orig, snippet, contMarker)
	if spliceErr == nil {
		return result, false, nil
	}
	if errors.Is(spliceErr, ErrFallthrough) {
		return NativeEdit(snippet), true, nil
	}
	return nil, false, spliceErr
}
