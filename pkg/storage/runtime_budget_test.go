package storage_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"oro/pkg/storage"
)

func TestRuntimeBudgetAdmissionBoundaries(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fresh parent: %v", err)
	}
	root := filepath.Join(parent, "runtime")
	sharedRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve shared root: %v", err)
	}
	beforeRoot, err := directoryEntries(parent)
	if err != nil {
		t.Fatalf("snapshot fresh parent: %v", err)
	}
	probe := func(context.Context, string) (storage.DiskUsage, error) {
		return storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, nil
	}

	exact, err := storage.CheckRuntimeBudget(context.Background(), probe, storage.RuntimeBudgetRequest{
		Root: root, RequiredBytes: 200, MinFreeBytes: 100, Fresh: true,
	})
	if err != nil {
		t.Fatalf("exact boundary rejected: %v", err)
	}
	if exact.Mode != storage.BudgetFresh || exact.Root != root || exact.FreeBytes != 300 {
		t.Fatalf("unexpected exact decision: %+v", exact)
	}
	if got, err := directoryEntries(parent); err != nil {
		t.Fatalf("snapshot fresh parent after exact check: %v", err)
	} else if !reflect.DeepEqual(got, beforeRoot) {
		t.Fatalf("exact check mutated filesystem: before=%v after=%v", beforeRoot, got)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact check created root %q: %v", root, err)
	}

	insufficient, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
		return storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 299}, nil
	}, storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 200, MinFreeBytes: 100, Fresh: true})
	wantInsufficient := storage.BudgetDecision{
		Mode: storage.BudgetDenied, Root: root, RequiredBytes: 200, FreeBytes: 299, MinFreeBytes: 100,
	}
	if err != nil || !reflect.DeepEqual(insufficient, wantInsufficient) {
		t.Fatalf("insufficient budget = %+v, err=%v; want %+v", insufficient, err, wantInsufficient)
	}

	for _, test := range []struct {
		name    string
		request storage.RuntimeBudgetRequest
		usage   storage.DiskUsage
		want    storage.BudgetDecision
	}{
		{
			name:    "minimum consumes all free bytes",
			request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 0, MinFreeBytes: 300, Fresh: true},
			usage:   storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300},
			want:    storage.BudgetDecision{Mode: storage.BudgetFresh, Root: root, RequiredBytes: 0, FreeBytes: 300, MinFreeBytes: 300},
		},
		{
			name:    "zero minimum exact boundary",
			request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 300, MinFreeBytes: 0, Fresh: true},
			usage:   storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300},
			want:    storage.BudgetDecision{Mode: storage.BudgetFresh, Root: root, RequiredBytes: 300, FreeBytes: 300, MinFreeBytes: 0},
		},
		{
			name:    "minimum exceeds free bytes",
			request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 0, MinFreeBytes: 301, Fresh: true},
			usage:   storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300},
			want:    storage.BudgetDecision{Mode: storage.BudgetDenied, Root: root, RequiredBytes: 0, FreeBytes: 300, MinFreeBytes: 301},
		},
		{
			name:    "maximum signed boundary",
			request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: math.MaxInt64 - 1, MinFreeBytes: 1, Fresh: true},
			usage:   storage.DiskUsage{CapacityBytes: math.MaxInt64, FreeBytes: math.MaxInt64},
			want: storage.BudgetDecision{
				Mode: storage.BudgetFresh, Root: root, RequiredBytes: math.MaxInt64 - 1,
				FreeBytes: math.MaxInt64, MinFreeBytes: 1,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
				return test.usage, nil
			}, test.request)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("budget decision = %+v, err=%v; want %+v", got, err, test.want)
			}
		})
	}

	shared, err := storage.CheckRuntimeBudget(context.Background(), probe, storage.RuntimeBudgetRequest{
		Root: sharedRoot, RequiredBytes: 200, MinFreeBytes: 100, Fresh: false,
	})
	if err != nil || shared.Mode != storage.BudgetShared || shared.Root != sharedRoot {
		t.Fatalf("shared canonical root decision = %+v, err=%v", shared, err)
	}
	sharedBefore, err := directoryEntries(sharedRoot)
	if err != nil {
		t.Fatalf("snapshot shared root: %v", err)
	}
	shared, err = storage.CheckRuntimeBudget(context.Background(), probe, storage.RuntimeBudgetRequest{
		Root: sharedRoot, RequiredBytes: 200, MinFreeBytes: 100, Fresh: false,
	})
	if err != nil || shared.Mode != storage.BudgetShared || shared.Root != sharedRoot {
		t.Fatalf("shared canonical root repeat decision = %+v, err=%v", shared, err)
	}
	if got, err := directoryEntries(sharedRoot); err != nil {
		t.Fatalf("snapshot shared root after check: %v", err)
	} else if !reflect.DeepEqual(got, sharedBefore) {
		t.Fatalf("shared check mutated filesystem: before=%v after=%v", sharedBefore, got)
	}
	symlinkRoot := filepath.Join(parent, "shared-alias")
	if err := os.Symlink(sharedRoot, symlinkRoot); err != nil {
		t.Fatalf("create shared-root symlink: %v", err)
	}
	symlinkDecision, err := storage.CheckRuntimeBudget(context.Background(), probe, storage.RuntimeBudgetRequest{
		Root: symlinkRoot, RequiredBytes: 1, MinFreeBytes: 1, Fresh: false,
	})
	var symlinkBudgetErr *storage.RuntimeBudgetError
	if symlinkDecision.Mode != storage.BudgetDenied || !errors.As(err, &symlinkBudgetErr) {
		t.Fatalf("shared symlink decision = %+v, err=%v; want typed denial", symlinkDecision, err)
	}
	freshAliasParent := filepath.Join(parent, "fresh-alias")
	if err := os.Symlink(sharedRoot, freshAliasParent); err != nil {
		t.Fatalf("create fresh parent symlink: %v", err)
	}
	freshAlias, err := storage.CheckRuntimeBudget(context.Background(), probe, storage.RuntimeBudgetRequest{
		Root: filepath.Join(freshAliasParent, "new-root"), RequiredBytes: 1, MinFreeBytes: 1, Fresh: true,
	})
	if freshAlias.Mode != storage.BudgetDenied || !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
		t.Fatalf("fresh symlink-parent decision = %+v, err=%v; want typed denial", freshAlias, err)
	}

	existingFreshRoot := filepath.Join(parent, "existing-fresh")
	if err := os.Mkdir(existingFreshRoot, 0o700); err != nil {
		t.Fatalf("create existing fresh root: %v", err)
	}
	sharedFile := filepath.Join(parent, "shared-file")
	if err := os.WriteFile(sharedFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create shared file: %v", err)
	}
	for _, test := range []struct {
		name         string
		request      storage.RuntimeBudgetRequest
		wantFragment string
	}{
		{
			name: "fresh missing parent",
			request: storage.RuntimeBudgetRequest{
				Root: filepath.Join(parent, "missing-parent", "runtime"), RequiredBytes: 1, MinFreeBytes: 1, Fresh: true,
			},
			wantFragment: "root parent is unavailable",
		},
		{
			name:         "fresh root exists",
			request:      storage.RuntimeBudgetRequest{Root: existingFreshRoot, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true},
			wantFragment: "fresh runtime root already exists",
		},
		{
			name: "fresh root inspection fails",
			request: storage.RuntimeBudgetRequest{
				Root: filepath.Join(parent, "runtime\x00invalid"), RequiredBytes: 1, MinFreeBytes: 1, Fresh: true,
			},
			wantFragment: "inspect fresh runtime root",
		},
		{
			name:         "shared root missing",
			request:      storage.RuntimeBudgetRequest{Root: filepath.Join(parent, "missing-shared"), RequiredBytes: 1, MinFreeBytes: 1},
			wantFragment: "shared runtime root is not an existing directory",
		},
		{
			name:         "shared root is file",
			request:      storage.RuntimeBudgetRequest{Root: sharedFile, RequiredBytes: 1, MinFreeBytes: 1},
			wantFragment: "shared runtime root is not an existing directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			probeCalls := 0
			decision, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
				probeCalls++
				return storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, nil
			}, test.request)
			wantDecision := storage.BudgetDecision{
				Mode: storage.BudgetDenied, Root: test.request.Root,
				RequiredBytes: test.request.RequiredBytes, MinFreeBytes: test.request.MinFreeBytes,
			}
			if !reflect.DeepEqual(decision, wantDecision) || probeCalls != 0 {
				t.Fatalf("root policy decision = %+v, probe calls=%d; want %+v, calls=0", decision, probeCalls, wantDecision)
			}
			assertRuntimeBudgetError(t, err, "request", storage.ErrRuntimeBudgetInvalid)
			if !strings.Contains(err.Error(), test.wantFragment) {
				t.Fatalf("root policy error = %v, want fragment %q", err, test.wantFragment)
			}
		})
	}

	invalidCases := []struct {
		name       string
		request    storage.RuntimeBudgetRequest
		usage      storage.DiskUsage
		wantReason string
		wantProbe  int
	}{
		{name: "negative required", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: -1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, wantReason: "request"},
		{name: "negative minimum", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: -1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, wantReason: "request"},
		{name: "probe zero capacity", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{}, wantReason: "probe", wantProbe: 1},
		{name: "probe capacity overflow", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: uint64(math.MaxInt64) + 1, FreeBytes: 1}, wantReason: "probe", wantProbe: 1},
		{name: "probe free overflow", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: uint64(math.MaxInt64), FreeBytes: uint64(math.MaxInt64) + 1}, wantReason: "probe", wantProbe: 1},
		{name: "free exceeds capacity", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 100, FreeBytes: 101}, wantReason: "probe", wantProbe: 1},
		{name: "required plus minimum overflow", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: math.MaxInt64, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: uint64(math.MaxInt64), FreeBytes: uint64(math.MaxInt64)}, wantReason: "request"},
		{name: "relative root", request: storage.RuntimeBudgetRequest{Root: "runtime", RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, wantReason: "request"},
		{name: "blank root", request: storage.RuntimeBudgetRequest{Root: "   ", RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, wantReason: "request"},
		{name: "non-clean root", request: storage.RuntimeBudgetRequest{Root: root + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(root), RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, wantReason: "request"},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			probeCalls := 0
			decision, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
				probeCalls++
				return test.usage, nil
			}, test.request)
			wantDecision := storage.BudgetDecision{
				Mode: storage.BudgetDenied, Root: test.request.Root,
				RequiredBytes: test.request.RequiredBytes, MinFreeBytes: test.request.MinFreeBytes,
			}
			if !reflect.DeepEqual(decision, wantDecision) || probeCalls != test.wantProbe {
				t.Fatalf("invalid budget decision = %+v, probe calls=%d; want %+v, calls=%d", decision, probeCalls, wantDecision, test.wantProbe)
			}
			assertRuntimeBudgetError(t, err, test.wantReason, storage.ErrRuntimeBudgetInvalid)
		})
	}

	nilProbeRequest := storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}
	nilProbeDecision, err := storage.CheckRuntimeBudget(context.Background(), nil, nilProbeRequest)
	if !reflect.DeepEqual(nilProbeDecision, storage.BudgetDecision{
		Mode: storage.BudgetDenied, Root: root, RequiredBytes: 1, MinFreeBytes: 1,
	}) {
		t.Fatalf("nil probe decision = %+v", nilProbeDecision)
	}
	assertRuntimeBudgetError(t, err, "probe", storage.ErrRuntimeBudgetInvalid)

	probeUnavailable := errors.New("probe unavailable")
	unknownRequest := storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}
	unknown, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
		return storage.DiskUsage{}, probeUnavailable
	}, storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true})
	if !reflect.DeepEqual(unknown, storage.BudgetDecision{
		Mode: storage.BudgetDenied, Root: unknownRequest.Root,
		RequiredBytes: unknownRequest.RequiredBytes, MinFreeBytes: unknownRequest.MinFreeBytes,
	}) {
		t.Fatalf("unknown probe = %+v, err=%v; want denied error", unknown, err)
	}
	assertRuntimeBudgetError(t, err, "probe", probeUnavailable)

	var canceledProbeCalls int
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := storage.CheckRuntimeBudget(canceledCtx, func(context.Context, string) (storage.DiskUsage, error) {
		canceledProbeCalls++
		return storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 1000}, nil
	}, storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true})
	if canceled.Mode != storage.BudgetDenied || !errors.Is(err, context.Canceled) || !errors.Is(err, storage.ErrRuntimeBudgetDenied) || canceledProbeCalls != 0 {
		t.Fatalf("canceled budget = %+v, err=%v, probe calls=%d", canceled, err, canceledProbeCalls)
	}
	assertRuntimeBudgetError(t, err, "context", context.Canceled)

	t.Setenv("GOCACHE", filepath.Join(parent, "hostile-go-cache"))
	t.Setenv("GOMODCACHE", filepath.Join(parent, "hostile-mod-cache"))
	t.Setenv("GOTMPDIR", filepath.Join(parent, "hostile-tmp"))
	t.Setenv("ORO_SUBPROCESS_TMP_ROOT", filepath.Join(parent, "hostile-subprocess-root"))
	var observedRoot string
	envDecision, err := storage.CheckRuntimeBudget(context.Background(), func(_ context.Context, gotRoot string) (storage.DiskUsage, error) {
		observedRoot = gotRoot
		return storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}, nil
	}, storage.RuntimeBudgetRequest{Root: sharedRoot, RequiredBytes: 200, MinFreeBytes: 100, Fresh: false})
	if err != nil || envDecision.Mode != storage.BudgetShared || envDecision.Root != sharedRoot || observedRoot != sharedRoot {
		t.Fatalf("hostile environment changed budget decision = %+v, observed root=%q, err=%v", envDecision, observedRoot, err)
	}
}

func assertRuntimeBudgetError(t *testing.T, err error, reason string, cause error) {
	t.Helper()
	var budgetErr *storage.RuntimeBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("runtime budget error = %v, want *RuntimeBudgetError", err)
	}
	if budgetErr.Reason != reason {
		t.Fatalf("runtime budget reason = %q, want %q", budgetErr.Reason, reason)
	}
	if !errors.Is(err, storage.ErrRuntimeBudgetDenied) || !errors.Is(err, cause) {
		t.Fatalf("runtime budget error = %v, want denial and cause %v", err, cause)
	}
	wantMessage := "runtime budget " + reason + ": "
	if got := err.Error(); len(got) < len(wantMessage) || got[:len(wantMessage)] != wantMessage {
		t.Fatalf("runtime budget error message = %q, want prefix %q", got, wantMessage)
	}
}

func directoryEntries(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}
