//go:build !darwin && !linux

package storage

import "io/fs"

func sameDevice(first, second fs.FileInfo) bool {
	return false
}
