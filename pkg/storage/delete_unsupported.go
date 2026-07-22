//go:build !darwin && !linux

package storage

import (
	"fmt"
	"io/fs"
)

func sameDevice(first, second fs.FileInfo) bool {
	return false
}

func openTombstoneAnchor(tombstoneBoundary) (tombstoneAnchor, error) {
	return nil, fmt.Errorf("anchored tombstone deletion is unsupported on this platform")
}
