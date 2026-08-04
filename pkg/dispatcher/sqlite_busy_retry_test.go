//nolint:testpackage // Exercises the package-private retry primitive directly.
package dispatcher

import (
	"context"
	"errors"
	"testing"
)

func TestRetrySQLiteBusyOperation(t *testing.T) {
	t.Run("transient shared-cache lock", func(t *testing.T) {
		attempts := 0
		err := retrySQLiteBusyOperation(t.Context(), func() error {
			attempts++
			if attempts < 4 {
				return errors.New("database table is locked")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retry operation: %v", err)
		}
		if attempts != 4 {
			t.Fatalf("attempts = %d, want 4", attempts)
		}
	})

	t.Run("non-lock error", func(t *testing.T) {
		want := errors.New("permanent failure")
		attempts := 0
		err := retrySQLiteBusyOperation(t.Context(), func() error {
			attempts++
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := retrySQLiteBusyOperation(ctx, func() error {
			return errors.New("database table is locked")
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
}
