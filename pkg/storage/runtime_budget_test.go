package storage_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	if err != nil || insufficient.Mode != storage.BudgetDenied {
		t.Fatalf("insufficient budget = %+v, err=%v; want denied", insufficient, err)
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

	invalidCases := []struct {
		name       string
		request    storage.RuntimeBudgetRequest
		usage      storage.DiskUsage
		wantDenied bool
	}{
		{name: "negative required", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: -1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}},
		{name: "negative minimum", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: -1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}},
		{name: "probe capacity overflow", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: uint64(math.MaxInt64) + 1, FreeBytes: 1}},
		{name: "probe free overflow", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: uint64(math.MaxInt64) + 1, FreeBytes: uint64(math.MaxInt64) + 1}},
		{name: "free exceeds capacity", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 100, FreeBytes: 101}},
		{name: "required plus minimum overflow", request: storage.RuntimeBudgetRequest{Root: root, RequiredBytes: math.MaxInt64, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: uint64(math.MaxInt64), FreeBytes: uint64(math.MaxInt64)}, wantDenied: true},
		{name: "relative root", request: storage.RuntimeBudgetRequest{Root: "runtime", RequiredBytes: 1, MinFreeBytes: 1, Fresh: true}, usage: storage.DiskUsage{CapacityBytes: 1000, FreeBytes: 300}},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			decision, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
				return test.usage, nil
			}, test.request)
			var budgetErr *storage.RuntimeBudgetError
			if decision.Mode != storage.BudgetDenied || !errors.As(err, &budgetErr) {
				t.Fatalf("invalid budget decision = %+v, err=%v; want denied error", decision, err)
			}
			if !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
				t.Fatalf("invalid budget error = %v, want ErrRuntimeBudgetDenied", err)
			}
		})
	}

	unknown, err := storage.CheckRuntimeBudget(context.Background(), func(context.Context, string) (storage.DiskUsage, error) {
		return storage.DiskUsage{}, errors.New("probe unavailable")
	}, storage.RuntimeBudgetRequest{Root: root, RequiredBytes: 1, MinFreeBytes: 1, Fresh: true})
	var unknownBudgetErr *storage.RuntimeBudgetError
	if unknown.Mode != storage.BudgetDenied || !errors.As(err, &unknownBudgetErr) || !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
		t.Fatalf("unknown probe = %+v, err=%v; want denied error", unknown, err)
	}

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
