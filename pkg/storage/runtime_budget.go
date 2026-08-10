package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// DiskProbe reports filesystem capacity and currently available bytes for a
// candidate runtime root. It must not create or modify the root.
type DiskProbe func(context.Context, string) (DiskUsage, error)

// DiskUsage is the raw filesystem headroom observed by a DiskProbe.
type DiskUsage struct {
	CapacityBytes uint64
	FreeBytes     uint64
}

// BudgetMode identifies the allocation policy selected by the admission check.
type BudgetMode string

const (
	// BudgetFresh allocates a new isolated root.
	BudgetFresh BudgetMode = "fresh"
	// BudgetShared reuses a caller-authorized root.
	BudgetShared BudgetMode = "shared"
	// BudgetDenied creates neither a fresh nor shared root.
	BudgetDenied BudgetMode = "denied"
)

// RuntimeBudgetRequest describes the bytes and root policy needed by one
// runtime. A non-fresh root must already be an existing, canonical directory
// supplied by the caller as a pre-authorized shared cache.
type RuntimeBudgetRequest struct {
	Root          string
	RequiredBytes int64
	MinFreeBytes  int64
	Fresh         bool
}

// BudgetDecision is the deterministic result consumed by runtime allocation.
type BudgetDecision struct {
	Mode          BudgetMode
	Root          string
	RequiredBytes int64
	FreeBytes     int64
	MinFreeBytes  int64
}

var (
	// ErrRuntimeBudgetDenied means the filesystem is valid but cannot satisfy
	// the requested headroom.
	ErrRuntimeBudgetDenied = errors.New("runtime budget denied")
	// ErrRuntimeBudgetInvalid means the request or probe returned malformed data.
	ErrRuntimeBudgetInvalid = errors.New("invalid runtime budget")
)

// RuntimeBudgetError identifies a fail-closed budget admission failure while
// preserving errors.Is for the underlying denial, cancellation, or probe error.
type RuntimeBudgetError struct {
	Reason string
	Err    error
}

func (err *RuntimeBudgetError) Error() string {
	return fmt.Sprintf("runtime budget %s: %v", err.Reason, err.Err)
}

func (err *RuntimeBudgetError) Unwrap() error { return err.Err }

// CheckRuntimeBudget validates a request, probes its filesystem, and returns a
// decision without creating roots or consulting environment overrides.
func CheckRuntimeBudget(ctx context.Context, probe DiskProbe, req RuntimeBudgetRequest) (BudgetDecision, error) {
	denied := BudgetDecision{Mode: BudgetDenied, Root: req.Root, RequiredBytes: req.RequiredBytes, MinFreeBytes: req.MinFreeBytes}
	if err := ctx.Err(); err != nil {
		return denied, deniedRuntimeBudgetError("context", err)
	}
	if probe == nil {
		return denied, deniedRuntimeBudgetError("probe", ErrRuntimeBudgetInvalid)
	}
	if err := validateRuntimeBudgetRequest(req); err != nil {
		return denied, deniedRuntimeBudgetError("request", err)
	}
	usage, err := probe(ctx, req.Root)
	if err != nil {
		return denied, deniedRuntimeBudgetError("probe", err)
	}
	if err := validateDiskUsage(usage); err != nil {
		return denied, deniedRuntimeBudgetError("probe", err)
	}
	denied.FreeBytes = int64(usage.FreeBytes)
	if uint64(req.MinFreeBytes) > usage.FreeBytes || usage.FreeBytes-uint64(req.MinFreeBytes) < uint64(req.RequiredBytes) {
		return denied, nil
	}
	mode := BudgetShared
	if req.Fresh {
		mode = BudgetFresh
	}
	return BudgetDecision{
		Mode:          mode,
		Root:          req.Root,
		RequiredBytes: req.RequiredBytes,
		FreeBytes:     int64(usage.FreeBytes),
		MinFreeBytes:  req.MinFreeBytes,
	}, nil
}

func deniedRuntimeBudgetError(reason string, cause error) error {
	return &RuntimeBudgetError{Reason: reason, Err: errors.Join(ErrRuntimeBudgetDenied, cause)}
}

func validateRuntimeBudgetRequest(req RuntimeBudgetRequest) error {
	if strings.TrimSpace(req.Root) == "" || !filepath.IsAbs(req.Root) || filepath.Clean(req.Root) != req.Root {
		return ErrRuntimeBudgetInvalid
	}
	if req.RequiredBytes < 0 || req.MinFreeBytes < 0 {
		return ErrRuntimeBudgetInvalid
	}
	if req.RequiredBytes > math.MaxInt64-req.MinFreeBytes {
		return fmt.Errorf("%w: required and minimum bytes overflow", ErrRuntimeBudgetInvalid)
	}
	return validateRuntimeBudgetRoot(req)
}

func validateRuntimeBudgetRoot(req RuntimeBudgetRequest) error {
	parentInfo, err := os.Stat(filepath.Dir(req.Root))
	if err != nil || !parentInfo.IsDir() {
		return fmt.Errorf("%w: runtime budget root parent is unavailable", ErrRuntimeBudgetInvalid)
	}
	if req.Fresh {
		parent := filepath.Dir(req.Root)
		canonicalParent, err := filepath.EvalSymlinks(parent)
		if err != nil || canonicalParent != parent {
			return fmt.Errorf("%w: fresh runtime root parent is not canonical", ErrRuntimeBudgetInvalid)
		}
		if _, err := os.Lstat(req.Root); err == nil {
			return fmt.Errorf("%w: fresh runtime root already exists", ErrRuntimeBudgetInvalid)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect fresh runtime root: %w", ErrRuntimeBudgetInvalid, err)
		}
		return nil
	}
	info, err := os.Stat(req.Root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: shared runtime root is not an existing directory", ErrRuntimeBudgetInvalid)
	}
	canonical, err := filepath.EvalSymlinks(req.Root)
	if err != nil || canonical != req.Root {
		return fmt.Errorf("%w: shared runtime root is not canonical", ErrRuntimeBudgetInvalid)
	}
	return nil
}

func validateDiskUsage(usage DiskUsage) error {
	maxInt64 := uint64(math.MaxInt64)
	if usage.CapacityBytes == 0 || usage.CapacityBytes > maxInt64 || usage.FreeBytes > maxInt64 || usage.FreeBytes > usage.CapacityBytes {
		return ErrRuntimeBudgetInvalid
	}
	return nil
}
