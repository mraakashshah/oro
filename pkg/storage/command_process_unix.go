//go:build unix

package storage

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func configureLeasedCommandProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill leased command process group: %w", err)
		}
		return nil
	}
}
