package storage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// ScratchWarningBytes is the per-namespace threshold for a storage warning.
	//
	//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
	ScratchWarningBytes int64 = 256 << 20
	// ScratchStopBytes is the per-namespace threshold that stops new subprocesses.
	//
	//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
	ScratchStopBytes int64 = 512 << 20
	// ScratchTargetBytes is the aggregate Oro-managed scratch cleanup target.
	//
	//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
	ScratchTargetBytes int64 = 2 << 30
	// ScratchCeilingBytes is the aggregate admission ceiling for Oro-managed scratch.
	//
	//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
	ScratchCeilingBytes int64 = 3 << 30
)

// ScratchState is the most severe threshold reached by a scratch measurement.
//
//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
type ScratchState string

const (
	// ScratchNormal means no threshold has been reached.
	ScratchNormal ScratchState = "normal"
	// ScratchWarning means a namespace has reached the warning threshold.
	ScratchWarning ScratchState = "warning"
	// ScratchStop means a namespace has reached the subprocess stop threshold.
	ScratchStop ScratchState = "stop"
	// ScratchTarget means aggregate scratch should be reduced to its target.
	ScratchTarget ScratchState = "target"
	// ScratchCeiling means aggregate scratch has reached the admission ceiling.
	ScratchCeiling ScratchState = "ceiling"
)

// ScratchNamespace identifies one Oro-owned worktree scratch directory.
//
//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
type ScratchNamespace struct {
	ID   string
	Path string
}

// ScratchPaths separates worktree scratch from external shared cache paths.
// SharedExternalCaches are retained for reporting provenance and are never
// included in scratch usage totals.
//
//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
type ScratchPaths struct {
	Namespaces           []ScratchNamespace
	SharedExternalCaches []string
}

// NamespaceScratchUsage is the measured result for one scratch namespace.
//
//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
type NamespaceScratchUsage struct {
	ScratchNamespace
	Bytes int64
	State ScratchState
}

// ScratchUsage is the measured per-namespace and aggregate scratch usage.
//
//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
type ScratchUsage struct {
	Namespaces     []NamespaceScratchUsage
	AggregateBytes int64
	AggregateState ScratchState
}

// MeasureScratchUsage measures only the provided Oro-owned scratch namespaces.
// It intentionally excludes shared external cache paths from all byte totals.
//
//oro:testonly — runtime enforcement wiring lands in the storage lifecycle task.
func MeasureScratchUsage(paths ScratchPaths) (ScratchUsage, error) {
	usage := ScratchUsage{
		Namespaces:     make([]NamespaceScratchUsage, 0, len(paths.Namespaces)),
		AggregateState: ScratchNormal,
	}
	for _, namespace := range paths.Namespaces {
		bytes, err := scratchPathBytes(namespace.Path)
		if err != nil {
			return ScratchUsage{}, fmt.Errorf("measure scratch namespace %q: %w", namespace.ID, err)
		}
		usage.Namespaces = append(usage.Namespaces, NamespaceScratchUsage{
			ScratchNamespace: namespace,
			Bytes:            bytes,
			State:            namespaceScratchState(bytes),
		})
		usage.AggregateBytes += bytes
	}
	usage.AggregateState = aggregateScratchState(usage.AggregateBytes)
	return usage, nil
}

func namespaceScratchState(bytes int64) ScratchState {
	switch {
	case bytes >= ScratchStopBytes:
		return ScratchStop
	case bytes >= ScratchWarningBytes:
		return ScratchWarning
	default:
		return ScratchNormal
	}
}

func aggregateScratchState(bytes int64) ScratchState {
	switch {
	case bytes >= ScratchCeilingBytes:
		return ScratchCeiling
	case bytes >= ScratchTargetBytes:
		return ScratchTarget
	default:
		return ScratchNormal
	}
}

func scratchPathBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return info.Size(), nil
	}

	var bytes int64
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read scratch entry info: %w", err)
		}
		bytes += entryInfo.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk %s: %w", path, err)
	}
	return bytes, nil
}
