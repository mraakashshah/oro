package dbutil

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type sqliteCodeError int

func (e sqliteCodeError) Error() string { return fmt.Sprintf("sqlite error %d", e) }
func (e sqliteCodeError) Code() int     { return int(e) }

func TestRetrySQLiteBusyStopsAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	started := time.Now()
	calls := 0
	err := retrySQLiteBusy(ctx, func() error {
		calls++
		return sqliteCodeError(261) // SQLITE_BUSY_RECOVERY
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retrySQLiteBusy error = %v, want context deadline exceeded", err)
	}
	if calls < 2 {
		t.Errorf("retrySQLiteBusy calls = %d, want at least 2", calls)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Errorf("retrySQLiteBusy elapsed = %v, want <= 250ms", elapsed)
	}
}
