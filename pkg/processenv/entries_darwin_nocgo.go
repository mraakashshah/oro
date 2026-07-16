//go:build darwin && !cgo

package processenv

import "fmt"

// ReadEntries fails closed when the Darwin sysctl wrapper is unavailable.
func ReadEntries(pid int) ([]string, error) {
	return nil, fmt.Errorf("read process environment entries for pid %d: cgo disabled", pid)
}
