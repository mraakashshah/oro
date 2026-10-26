package factory

import (
	"errors"
	"fmt"
)

type managedInterruptionError struct {
	stage string
	cause error
}

func (e *managedInterruptionError) Error() string {
	return fmt.Sprintf("managed %s interrupted: %v", e.stage, e.cause)
}
func (e *managedInterruptionError) Unwrap() error { return e.cause }

func isManagedInterruption(err error) bool {
	var interruption *managedInterruptionError
	return errors.As(err, &interruption)
}
