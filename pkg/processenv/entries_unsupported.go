//go:build !darwin && !linux

package processenv

import (
	"errors"
	"fmt"
)

// ReadEntries fails closed where no delimiter-preserving reader is available.
func ReadEntries(pid int) ([]string, error) {
	return nil, fmt.Errorf("read process environment entries for pid %d: %w", pid, errors.ErrUnsupported)
}
