//go:build !darwin && !linux

package storage

import (
	"fmt"
	"os"
)

func signalProcessGroup(processGroup int, signal os.Signal) error {
	return fmt.Errorf("process-group signals are unsupported for process group %d and signal %v", processGroup, signal)
}
