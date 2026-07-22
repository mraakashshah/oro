package remotegate_test

import (
	"errors"
	"fmt"
	"testing"

	"oro/pkg/remotegate"
)

func TestWorkflowIneligibleErrorContract(t *testing.T) {
	t.Parallel()

	if remotegate.ErrWorkflowIneligible == nil {
		t.Fatal("ErrWorkflowIneligible must be non-nil")
	}

	wrapped := fmt.Errorf("parse workflow: %w", fmt.Errorf("workflow check: %w", remotegate.ErrWorkflowIneligible))
	for name, err := range map[string]error{
		"direct":  remotegate.ErrWorkflowIneligible,
		"wrapped": wrapped,
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
				t.Fatalf("errors.Is(%v, ErrWorkflowIneligible) = false", err)
			}
		})
	}

	if errors.Is(errors.New("workflow ineligible"), remotegate.ErrWorkflowIneligible) {
		t.Fatal("unrelated error must not match ErrWorkflowIneligible")
	}
}
