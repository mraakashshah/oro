//go:build darwin

package main

import (
	"os"
	"syscall"
)

// factoryCredentialCaseSensitive queries the existing directory's filesystem.
func factoryCredentialCaseSensitive(directory *os.File) (sensitive, known bool) {
	const pcCaseSensitive = 11 // Darwin _PC_CASE_SENSITIVE
	value, err := syscall.Fpathconf(int(directory.Fd()), pcCaseSensitive)
	if err != nil || (value != 0 && value != 1) {
		return false, false
	}
	return value == 1, true
}
