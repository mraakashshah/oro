//go:build darwin || linux

package storage

import (
	"io/fs"
	"syscall"
)

func sameDevice(first, second fs.FileInfo) bool {
	firstStat, firstOK := first.Sys().(*syscall.Stat_t)
	secondStat, secondOK := second.Sys().(*syscall.Stat_t)
	return firstOK && secondOK && firstStat.Dev == secondStat.Dev
}
