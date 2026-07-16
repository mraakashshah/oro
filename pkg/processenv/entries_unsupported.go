//go:build !darwin && !linux

package processenv

import "fmt"

// ReadEntries fails closed where no delimiter-preserving reader is available.
func ReadEntries(pid int) ([]string, error) {
	return nil, fmt.Errorf("read process environment entries for pid %d: unsupported OS", pid)
}
