//go:build darwin

package processenv

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ReadEntries returns exact environment entries from Darwin's kern.procargs2
// payload without requiring cgo. Unlike ps, this source retains NUL boundaries
// between values.
func ReadEntries(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, fmt.Errorf("read kern.procargs2 for pid %d: %w", pid, err)
	}
	entries, err := ParseDarwinEntries(raw)
	if err != nil {
		return nil, fmt.Errorf("parse kern.procargs2 for pid %d: %w", pid, err)
	}
	return entries, nil
}
