package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

var errInjected = errors.New("injected store fault")

// errorReadTx is a ReadTx that returns errInjected for every read method.
type errorReadTx struct{}

func (r *errorReadTx) Ready(_ context.Context) ([]protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) InProgress(_ context.Context) ([]protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) Blocked(_ context.Context) ([]protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) Closed(_ context.Context, _ int) ([]protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) Show(_ context.Context, _ string) (*protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) HasChildren(_ context.Context, _ string) (bool, error) {
	return false, errInjected
}

func (r *errorReadTx) AllChildrenClosed(_ context.Context, _ string) (bool, error) {
	return false, errInjected
}

func (r *errorReadTx) FindByParentAndTag(_ context.Context, _, _ string) ([]protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) FindByMetadataKey(_ context.Context, _ string) ([]*protocol.Bead, error) {
	return nil, errInjected
}

func (r *errorReadTx) Journey(_ context.Context, _ string, _ time.Time) ([]beadstore.JourneyEvent, error) {
	return nil, errInjected
}

func (r *errorReadTx) LatestJourney(_ context.Context, _ string, _ int) ([]beadstore.JourneyEvent, error) {
	return nil, errInjected
}
func (r *errorReadTx) Cards() cards.ReadTx { return nil }

// errorBeadStore is a beadstore.Store whose WithReadTx calls fn with an errorReadTx.
// All other methods are never reached by render commands (which go through WithReadTx).
type errorBeadStore struct {
	beadstore.Store // embedded nil; panics only if a non-WithReadTx method is called
}

func (s *errorBeadStore) WithReadTx(_ context.Context, fn func(beadstore.ReadTx) error) error {
	return fn(&errorReadTx{})
}

func runRenderCmdAndCapture(t *testing.T, cmd *cobra.Command, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestAllThreeFailClosedOnError(t *testing.T) {
	store := &errorBeadStore{}

	tests := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"current", newCurrentCmdWithStore(store), []string{"--format", "json"}},
		{"handoff", newHandoffCmdWithStore(store), []string{"--since", "1h"}},
		{"resume", newResumeCmdWithStore(store), []string{"bead-does-not-exist"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := runRenderCmdAndCapture(t, tt.cmd, tt.args)

			// exit code != 0: Execute must return a non-nil error.
			if err == nil {
				t.Fatalf("%s: expected error but Execute returned nil", tt.name)
			}

			// stderr must contain a structured error with an "ok" field.
			if !strings.Contains(stderr, `"ok"`) {
				t.Fatalf("%s: stderr missing structured error; got: %q", tt.name, stderr)
			}

			// No partial output on stdout.
			if stdout != "" {
				t.Fatalf("%s: stdout must be empty on error; got: %q", tt.name, stdout)
			}
		})
	}
}
