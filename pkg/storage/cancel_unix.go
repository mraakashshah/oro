//go:build darwin || linux

package storage

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func signalProcessGroup(processGroup int, signal os.Signal) error {
	if processGroup <= 0 {
		return fmt.Errorf("invalid process group %d", processGroup)
	}
	syscallSignal, ok := signal.(unix.Signal)
	if !ok {
		return fmt.Errorf("unsupported signal %v", signal)
	}
	if err := unix.Kill(-processGroup, syscallSignal); err != nil {
		return fmt.Errorf("signal process group %d: %w", processGroup, err)
	}
	return nil
}
