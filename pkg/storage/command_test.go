//nolint:testpackage // Exercises the injected lifecycle core to prove lease ordering and failed-start cleanup.
package storage

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunLeasedCommandOrdersLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		startErr  error
		wantTrace []string
	}{
		{
			name:      "success",
			wantTrace: []string{"resolve", "acquire", "start", "wait", "release"},
		},
		{
			name:      "start failure releases without child",
			startErr:  errors.New("start failed"),
			wantTrace: []string{"resolve", "acquire", "start", "release"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace := make([]string, 0, len(test.wantTrace))
			catalog := &recordingCommandLeaseCatalog{trace: &trace}
			command := &recordingLeasedCommand{
				trace:    &trace,
				catalog:  catalog,
				startErr: test.startErr,
			}
			var commandEnv []string
			request := CommandRequest{
				Runtime: RuntimeRequest{
					Catalog: catalog,
					Lease:   commandLeaseRequest(test.name),
					Env: []string{
						"ORO_SUBPROCESS_TMP_ROOT=" + t.TempDir(),
					},
					Workdir: t.TempDir(),
					Policy: StoragePolicy{Providers: []CacheProvider{{
						ID:        "trace-cache",
						Variables: []string{"TRACE_CACHE"},
						Scope:     ProjectScope,
						DefaultPath: func() string {
							trace = append(trace, "resolve")
							return filepath.Join(t.TempDir(), "cache")
						},
						Concurrency: Concurrent,
						Ownership:   OroManaged,
					}}},
				},
				Path: "test-command",
			}

			result, err := runLeasedCommandWithFactory(t.Context(), request, func(_ context.Context, _ CommandRequest, env []string) leasedCommand {
				commandEnv = append([]string(nil), env...)
				return command
			})
			assertCommandResult(t, result, err, command, test.startErr)
			if !command.startedWithLease {
				t.Fatal("command started without an active lease")
			}
			if value := commandEnvValue(commandEnv, "TRACE_CACHE"); value == "" {
				t.Fatalf("command environment did not include resolved cache: %v", commandEnv)
			}
			if value := commandEnvValue(commandEnv, "TMPDIR"); value == "" {
				t.Fatalf("command environment did not include runtime scratch: %v", commandEnv)
			}
			if !reflect.DeepEqual(trace, test.wantTrace) {
				t.Fatalf("lifecycle trace = %v, want %v", trace, test.wantTrace)
			}
		})
	}
}

func TestStartedCommandKillWrapsCancelError(t *testing.T) {
	cancelErr := errors.New("cancel failed")
	command := &StartedCommand{command: execCommand{command: &exec.Cmd{
		Cancel: func() error { return cancelErr },
	}}}

	err := command.Kill()
	if !errors.Is(err, cancelErr) {
		t.Fatalf("Kill() error = %v, want wrapped %v", err, cancelErr)
	}
	if !strings.Contains(err.Error(), "cancel leased command") {
		t.Fatalf("Kill() error = %q, want cancel context", err)
	}
}

type recordingCommandLeaseCatalog struct {
	trace  *[]string
	active bool
}

func (catalog *recordingCommandLeaseCatalog) AcquireLease(_ context.Context, request LeaseRequest) (Lease, error) {
	*catalog.trace = append(*catalog.trace, "acquire")
	catalog.active = true
	return Lease{LeaseRequest: request}, nil
}

func (catalog *recordingCommandLeaseCatalog) ReleaseLease(_ context.Context, _ LeaseID) error {
	*catalog.trace = append(*catalog.trace, "release")
	catalog.active = false
	return nil
}

type recordingLeasedCommand struct {
	trace            *[]string
	catalog          *recordingCommandLeaseCatalog
	startErr         error
	startedWithLease bool
	waited           bool
}

func (command *recordingLeasedCommand) start() error {
	*command.trace = append(*command.trace, "start")
	command.startedWithLease = command.catalog.active
	return command.startErr
}

func (command *recordingLeasedCommand) wait() error {
	*command.trace = append(*command.trace, "wait")
	command.waited = true
	return nil
}

func (command *recordingLeasedCommand) exitCode() int {
	return 0
}

func commandLeaseRequest(name string) LeaseRequest {
	now := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	return LeaseRequest{
		ID:           LeaseID("lease-" + name),
		Namespace:    "runtime-namespace",
		ControllerID: "controller",
		OwnerID:      "owner",
		PID:          1,
		ProcessStart: now,
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}
}

func commandEnvValue(env []string, want string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == want {
			return value
		}
	}
	return ""
}

func assertCommandResult(t *testing.T, result CommandResult, err error, command *recordingLeasedCommand, startErr error) {
	t.Helper()
	if startErr != nil {
		if !errors.Is(err, startErr) {
			t.Fatalf("RunLeasedCommand() error = %v, want %v", err, startErr)
		}
		if command.waited {
			t.Fatal("failed start exposed a child to Wait")
		}
		return
	}
	if err != nil {
		t.Fatalf("RunLeasedCommand() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("RunLeasedCommand() exit code = %d, want 0", result.ExitCode)
	}
}
