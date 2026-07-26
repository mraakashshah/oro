package storage_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"oro/pkg/storage"
)

type recordingRuntimeLeaseStore struct {
	lease        storage.Lease
	acquireCalls int
	releaseCalls int
}

func (s *recordingRuntimeLeaseStore) AcquireLease(_ context.Context, request storage.LeaseRequest) (storage.Lease, error) {
	s.acquireCalls++
	s.lease = storage.Lease{LeaseRequest: request}
	return s.lease, nil
}

func (s *recordingRuntimeLeaseStore) ReleaseLease(_ context.Context, id storage.LeaseID) error {
	if id != s.lease.ID {
		return errors.New("unexpected lease id")
	}
	s.releaseCalls++
	releasedAt := time.Now().UTC()
	s.lease.ReleasedAt = &releasedAt
	return nil
}

func TestRuntimeHandleLeaseEnvelope(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  func(context.Context, *storage.RuntimeHandle) error
	}{
		{
			name: "success",
			run: func(_ context.Context, _ *storage.RuntimeHandle) error {
				return nil
			},
		},
		{
			name: "spawn error",
			run: func(_ context.Context, _ *storage.RuntimeHandle) error {
				return errors.New("spawn failed")
			},
		},
		{
			name: "cancellation",
			run: func(ctx context.Context, _ *storage.RuntimeHandle) error {
				return ctx.Err()
			},
		},
		{
			name: "panic recovery",
			run: func(_ context.Context, _ *storage.RuntimeHandle) error {
				panic("spawn panic")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingRuntimeLeaseStore{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			handle, err := storage.OpenRuntime(ctx, storage.RuntimeRequest{
				Catalog: store,
				Lease: storage.LeaseRequest{
					ID:           storage.LeaseID("lease-" + test.name),
					Namespace:    "runtime-namespace",
					ControllerID: "controller",
					OwnerID:      "owner",
					PID:          1,
					ProcessStart: time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC),
					AcquiredAt:   time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC),
					HeartbeatAt:  time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC),
				},
				Env:     []string{"ORO_SUBPROCESS_TMP_ROOT=" + t.TempDir()},
				Workdir: t.TempDir(),
				Policy: storage.StoragePolicy{Providers: []storage.CacheProvider{{
					ID:          "go-cache",
					Variables:   []string{"GOCACHE"},
					Scope:       storage.ProjectScope,
					DefaultPath: func() string { return filepath.Join(t.TempDir(), "shared-cache") },
					Concurrency: storage.Concurrent,
					Ownership:   storage.OroManaged,
				}}},
			})
			if err != nil {
				t.Fatalf("open runtime: %v", err)
			}
			if store.acquireCalls != 1 || store.lease.ReleasedAt != nil {
				t.Fatalf("lease was not active before spawn: acquire=%d lease=%+v", store.acquireCalls, store.lease)
			}
			if handle.ScratchDir == "" {
				t.Fatal("runtime did not resolve scratch directory")
			}
			if value := runtimeEnvValue(handle.Env, "GOCACHE"); value == "" {
				t.Fatalf("runtime did not resolve GOCACHE: %v", handle.Env)
			}
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				if value := runtimeEnvValue(handle.Env, key); value != handle.ScratchDir {
					t.Fatalf("%s = %q, want scratch directory %q", key, value, handle.ScratchDir)
				}
			}

			var runErr error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						runErr = errors.New("spawn panic")
					}
				}()
				if test.name == "cancellation" {
					cancel()
				}
				runErr = test.run(ctx, handle)
			}()
			if store.lease.ReleasedAt != nil {
				t.Fatalf("lease was released before wait completed: %+v", store.lease)
			}
			if err := handle.Close(); err != nil {
				t.Fatalf("close runtime: %v", err)
			}
			if err := handle.Close(); err != nil {
				t.Fatalf("second close runtime: %v", err)
			}
			if store.releaseCalls != 1 || store.lease.ReleasedAt == nil {
				t.Fatalf("lease was not released exactly once: release=%d lease=%+v", store.releaseCalls, store.lease)
			}
			if test.name == "success" && runErr != nil {
				t.Fatalf("success run: %v", runErr)
			}
		})
	}
}

func TestRuntimeHandleUsesShortTmpRootOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin has a short Unix socket path limit")
	}
	if os.Getenv("ORO_RUNTIME_SHORT_TMP_HELPER") == "1" {
		assertRuntimeHandleUsesShortTmpRoot(t)
		return
	}

	longTmpRoot, err := os.MkdirTemp("/var/tmp", strings.Repeat("oro-runtime-short-tmp-", 3))
	if err != nil {
		t.Fatalf("create long tmp root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(longTmpRoot); err != nil {
			t.Errorf("remove long tmp root: %v", err)
		}
	})
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRuntimeHandleUsesShortTmpRootOnDarwin$")
	command.Env = []string{
		"ORO_RUNTIME_SHORT_TMP_HELPER=1",
		"TMPDIR=" + longTmpRoot,
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("short tmp root helper: %v\n%s", err, output)
	}
}

func assertRuntimeHandleUsesShortTmpRoot(t *testing.T) {
	t.Helper()

	store := &recordingRuntimeLeaseStore{}
	handle, err := storage.OpenRuntime(context.Background(), storage.RuntimeRequest{
		Catalog: store,
		Lease: storage.LeaseRequest{
			ID: storage.LeaseID("short-tmp-root"),
		},
		Workdir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	wantPrefix := filepath.Join("/tmp", "oro-subprocess") + string(filepath.Separator)
	if !strings.HasPrefix(handle.ScratchDir, wantPrefix) {
		t.Fatalf("scratch directory = %q, want short root prefix %q", handle.ScratchDir, wantPrefix)
	}
}

func runtimeEnvValue(env []string, want string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == want {
			return value
		}
	}
	return ""
}
