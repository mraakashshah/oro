package dispatcher

import (
	"context"
	"fmt"
	"time"
)

const maxSQLiteBusyOperationRetries = 20

func retrySQLiteBusyOperation(ctx context.Context, operation func() error) error {
	for attempt := 0; ; attempt++ {
		err := operation()
		if err == nil || !isSQLiteBusyError(err) {
			return err
		}
		if attempt >= maxSQLiteBusyOperationRetries {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("retry SQLite busy operation: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
