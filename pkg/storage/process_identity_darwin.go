//go:build darwin

package storage

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// InspectProcessIdentity returns the current Darwin identity for pid.
func InspectProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("invalid process id %d", pid)
	}

	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("inspect Darwin process %d: %w", pid, err)
	}
	if int(process.Proc.P_pid) != pid {
		return ProcessIdentity{}, fmt.Errorf("inspect Darwin process %d: returned pid %d", pid, process.Proc.P_pid)
	}

	executable := strings.TrimRight(string(process.Proc.P_comm[:]), "\x00")
	if executable == "" || process.Eproc.Pgid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("incomplete Darwin process identity for pid %d", pid)
	}

	start := process.Proc.P_starttime
	return ProcessIdentity{
		PID:          pid,
		StartMarker:  fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec),
		Executable:   executable,
		ProcessGroup: int(process.Eproc.Pgid),
	}, nil
}
