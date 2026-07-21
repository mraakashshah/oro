//go:build linux

package storage

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// InspectProcessIdentity returns the current Linux identity for pid.
func InspectProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("invalid process id %d", pid)
	}

	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	contents, err := os.ReadFile(statPath)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read Linux process stat for pid %d: %w", pid, err)
	}
	processGroup, startMarker, err := linuxProcessStatIdentity(contents)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("parse Linux process stat for pid %d: %w", pid, err)
	}

	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read Linux executable for pid %d: %w", pid, err)
	}
	if executable == "" || processGroup <= 0 {
		return ProcessIdentity{}, fmt.Errorf("incomplete Linux process identity for pid %d", pid)
	}

	return ProcessIdentity{
		PID:          pid,
		StartMarker:  "linux:" + startMarker,
		Executable:   executable,
		ProcessGroup: processGroup,
	}, nil
}

func linuxProcessStatIdentity(contents []byte) (int, string, error) {
	stat := string(contents)
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 || len(stat) <= closeParen+2 {
		return 0, "", fmt.Errorf("missing process name terminator")
	}
	fields := strings.Fields(stat[closeParen+2:])
	const (
		processGroupIndex = 2  // field 5, after state (field 3)
		startTimeIndex    = 19 // field 22, after state (field 3)
	)
	if len(fields) <= startTimeIndex {
		return 0, "", fmt.Errorf("expected at least %d process fields, got %d", startTimeIndex+1, len(fields))
	}
	processGroup, err := strconv.Atoi(fields[processGroupIndex])
	if err != nil {
		return 0, "", fmt.Errorf("parse process group: %w", err)
	}
	if _, err := strconv.ParseUint(fields[startTimeIndex], 10, 64); err != nil {
		return 0, "", fmt.Errorf("parse start time: %w", err)
	}
	return processGroup, fields[startTimeIndex], nil
}
