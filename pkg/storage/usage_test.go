package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/storage"
)

func TestScratchThresholdTransitions(t *testing.T) {
	t.Parallel()

	const (
		miB = int64(1 << 20)
		giB = int64(1 << 30)
	)

	tests := []struct {
		name           string
		namespaceBytes []int64
		sharedCache    int64
		wantNamespaces []storage.ScratchState
		wantAggregate  storage.ScratchState
		wantBytes      int64
	}{
		{
			name:           "warning at 0.25 GiB",
			namespaceBytes: []int64{256 * miB},
			wantNamespaces: []storage.ScratchState{storage.ScratchWarning},
			wantAggregate:  storage.ScratchNormal,
			wantBytes:      256 * miB,
		},
		{
			name:           "stop at 0.5 GiB",
			namespaceBytes: []int64{512 * miB},
			wantNamespaces: []storage.ScratchState{storage.ScratchStop},
			wantAggregate:  storage.ScratchNormal,
			wantBytes:      512 * miB,
		},
		{
			name:           "target at 2 GiB",
			namespaceBytes: []int64{512 * miB, 1536 * miB},
			wantNamespaces: []storage.ScratchState{storage.ScratchStop, storage.ScratchStop},
			wantAggregate:  storage.ScratchTarget,
			wantBytes:      2 * giB,
		},
		{
			name:           "ceiling at 3 GiB",
			namespaceBytes: []int64{512 * miB, 2560 * miB},
			wantNamespaces: []storage.ScratchState{storage.ScratchStop, storage.ScratchStop},
			wantAggregate:  storage.ScratchCeiling,
			wantBytes:      3 * giB,
		},
		{
			name:           "shared external cache excluded",
			namespaceBytes: []int64{1},
			sharedCache:    4 * giB,
			wantNamespaces: []storage.ScratchState{storage.ScratchNormal},
			wantAggregate:  storage.ScratchNormal,
			wantBytes:      1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			paths := storage.ScratchPaths{}
			for index, bytes := range tt.namespaceBytes {
				path := filepath.Join(root, "scratch", string(rune('a'+index)))
				writeSizedFile(t, path, bytes)
				paths.Namespaces = append(paths.Namespaces, storage.ScratchNamespace{ID: path, Path: path})
			}
			if tt.sharedCache != 0 {
				cachePath := filepath.Join(root, "shared-cache")
				writeSizedFile(t, cachePath, tt.sharedCache)
				paths.SharedExternalCaches = []string{cachePath}
			}

			usage, err := storage.MeasureScratchUsage(paths)
			if err != nil {
				t.Fatalf("MeasureScratchUsage() error = %v", err)
			}
			if usage.AggregateBytes != tt.wantBytes {
				t.Errorf("AggregateBytes = %d, want %d", usage.AggregateBytes, tt.wantBytes)
			}
			if usage.AggregateState != tt.wantAggregate {
				t.Errorf("AggregateState = %q, want %q", usage.AggregateState, tt.wantAggregate)
			}
			if len(usage.Namespaces) != len(tt.wantNamespaces) {
				t.Fatalf("namespace results = %d, want %d", len(usage.Namespaces), len(tt.wantNamespaces))
			}
			for index, want := range tt.wantNamespaces {
				if got := usage.Namespaces[index].State; got != want {
					t.Errorf("namespace %d state = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func writeSizedFile(t *testing.T, path string, bytes int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := os.Truncate(path, bytes); err != nil {
		t.Fatalf("size %s: %v", path, err)
	}
}
