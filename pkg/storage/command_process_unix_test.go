//go:build unix

//nolint:testpackage // Exercises the private executor to prove its process-group ownership contract.
package storage

import (
	"context"
	"testing"
)

func TestNewExecLeasedCommandOwnsProcessGroup(t *testing.T) {
	command := newExecLeasedCommand(context.Background(), CommandRequest{Path: "ignored"}, nil)
	execCommand, ok := command.(execCommand)
	if !ok {
		t.Fatalf("newExecLeasedCommand() = %T, want execCommand", command)
	}
	if execCommand.command.SysProcAttr == nil || !execCommand.command.SysProcAttr.Setpgid {
		t.Fatal("leased command does not create an owned process group")
	}
	if execCommand.command.Cancel == nil {
		t.Fatal("leased command does not cancel its process group")
	}
}
