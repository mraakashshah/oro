//go:build !darwin && !linux

package storage

import "fmt"

// InspectProcessIdentity is unavailable on this platform.
func InspectProcessIdentity(pid int) (ProcessIdentity, error) {
	return ProcessIdentity{}, fmt.Errorf("process identity inspection is unsupported for pid %d", pid)
}
