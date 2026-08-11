package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

const runtimeManifestSchemaVersion = 1

// RootClass describes the purpose of a managed runtime root.
type RootClass string

// Managed runtime root classes.
const (
	RootCache    RootClass = "cache"
	RootTemp     RootClass = "temp"
	RootEvidence RootClass = "evidence"
)

// RootDisposition defines whether a managed root may be reclaimed.
type RootDisposition string

// Managed runtime root dispositions.
const (
	RootDisposable RootDisposition = "disposable"
	RootDurable    RootDisposition = "durable"
	RootShared     RootDisposition = "shared"
)

// ManagedRoot records one runtime-owned filesystem namespace.
type ManagedRoot struct {
	Path        string          `json:"path"`
	Class       RootClass       `json:"class"`
	Disposition RootDisposition `json:"disposition"`
}

// FinalizationOutcome describes how a runtime execution ended.
type FinalizationOutcome string

// Supported runtime finalization outcomes.
const (
	FinalizeSuccess     FinalizationOutcome = "success"
	FinalizeFailure     FinalizationOutcome = "failure"
	FinalizeCanceled    FinalizationOutcome = "canceled"
	FinalizePanic       FinalizationOutcome = "panic"
	FinalizeInterrupted FinalizationOutcome = "interrupted"
)

// FinalizationRequest supplies the evidence needed to finalize one runtime.
type FinalizationRequest struct {
	ReservationID       string              `json:"reservation_id"`
	ManifestPath        string              `json:"manifest_path"`
	SourceEvidence      string              `json:"source_evidence"`
	DurableEvidenceRoot string              `json:"durable_evidence_root"`
	ExpectedSHA256      string              `json:"expected_sha256"`
	Outcome             FinalizationOutcome `json:"outcome"`
}

// FinalizationReceipt records durable evidence and reclaimed storage.
type FinalizationReceipt struct {
	ReservationID       string        `json:"reservation_id"`
	ManifestPath        string        `json:"manifest_path"`
	State               ManifestState `json:"state"`
	EvidencePath        string        `json:"evidence_path"`
	EvidenceSHA256      string        `json:"evidence_sha256"`
	EvidenceBytes       int64         `json:"evidence_bytes"`
	LogicalRemovedBytes int64         `json:"logical_removed_bytes"`
	ReclaimedBytes      int64         `json:"reclaimed_bytes"`
	LeaseReleased       bool          `json:"lease_released"`
	PreservedRoots      []string      `json:"preserved_roots"`
}

// RuntimeFinalizer durably closes or interrupts an owned runtime.
type RuntimeFinalizer interface {
	Finalize(context.Context, FinalizationRequest) (FinalizationReceipt, error)
	Interrupt(context.Context, FinalizationRequest) error
}

// ManifestState is the durable lifecycle state of a runtime manifest.
type ManifestState string

// Runtime manifest lifecycle states.
const (
	ManifestAllocating  ManifestState = "allocating"
	ManifestActive      ManifestState = "active"
	ManifestFinalizing  ManifestState = "finalizing"
	ManifestFinalized   ManifestState = "finalized"
	ManifestInterrupted ManifestState = "interrupted"
	ManifestReclaimable ManifestState = "reclaimable"
)

// RuntimeManifest durably describes one managed runtime and its roots.
type RuntimeManifest struct {
	SchemaVersion  int             `json:"schema_version"`
	Identity       RuntimeIdentity `json:"identity"`
	ReservationID  string          `json:"reservation_id"`
	LeaseID        string          `json:"lease_id"`
	ManifestPath   string          `json:"manifest_path"`
	Roots          []ManagedRoot   `json:"roots"`
	State          ManifestState   `json:"state"`
	EvidencePath   string          `json:"evidence_path,omitempty"`
	EvidenceSHA256 string          `json:"evidence_sha256,omitempty"`
}

var lowercaseSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type runtimeManifestAtomicOps struct {
	createTemp func(string, string) (*os.File, error)
	chmod      func(*os.File, os.FileMode) error
	write      func(*os.File, []byte) error
	sync       func(*os.File) error
	close      func(*os.File) error
	rename     func(string, string) error
	open       func(string) (*os.File, error)
	remove     func(string) error
}

func newRuntimeManifestAtomicOps() runtimeManifestAtomicOps {
	return runtimeManifestAtomicOps{
		createTemp: os.CreateTemp,
		chmod:      func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		write: func(file *os.File, contents []byte) error {
			if _, err := file.Write(contents); err != nil {
				return fmt.Errorf("write file: %w", err)
			}
			return nil
		},
		sync:   func(file *os.File) error { return file.Sync() },
		close:  func(file *os.File) error { return file.Close() },
		rename: os.Rename,
		open:   os.Open,
		remove: os.Remove,
	}
}

// WriteRuntimeManifestAtomic validates and durably publishes a manifest.
func WriteRuntimeManifestAtomic(path string, manifest RuntimeManifest) error {
	return writeRuntimeManifestAtomic(path, manifest, newRuntimeManifestAtomicOps())
}

func writeRuntimeManifestAtomic(path string, manifest RuntimeManifest, ops runtimeManifestAtomicOps) error {
	if err := validateRuntimeManifest(path, manifest); err != nil {
		return err
	}
	if err := validateManifestReplacement(path, manifest); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime manifest: %w", err)
	}
	contents = append(contents, '\n')
	return publishRuntimeManifestAtomic(path, contents, ops)
}

func validateManifestReplacement(path string, manifest RuntimeManifest) error {
	previous, err := ReadRuntimeManifest(path)
	if errors.Is(err, os.ErrNotExist) {
		if manifest.State != ManifestAllocating {
			return fmt.Errorf("initial runtime manifest state must be %s", ManifestAllocating)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read prior runtime manifest: %w", err)
	}
	if !validManifestTransition(previous.State, manifest.State) {
		return fmt.Errorf("invalid runtime manifest transition %s to %s", previous.State, manifest.State)
	}
	if !sameRuntimeManifestIdentity(previous, manifest) {
		return errors.New("runtime manifest identity and roots are immutable")
	}
	return nil
}

func publishRuntimeManifestAtomic(path string, contents []byte, ops runtimeManifestAtomicOps) error {
	parent := filepath.Dir(path)
	temporary, err := ops.createTemp(parent, ".runtime-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create runtime manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = ops.remove(temporaryPath)
		}
	}()
	if err := ops.chmod(temporary, 0o600); err != nil {
		_ = ops.close(temporary)
		return fmt.Errorf("chmod runtime manifest temporary file: %w", err)
	}
	if err := ops.write(temporary, contents); err != nil {
		_ = ops.close(temporary)
		return fmt.Errorf("write runtime manifest temporary file: %w", err)
	}
	if err := ops.sync(temporary); err != nil {
		_ = ops.close(temporary)
		return fmt.Errorf("sync runtime manifest temporary file: %w", err)
	}
	if err := ops.close(temporary); err != nil {
		return fmt.Errorf("close runtime manifest temporary file: %w", err)
	}
	if err := ops.rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish runtime manifest: %w", err)
	}
	removeTemporary = false
	directory, err := ops.open(parent)
	if err != nil {
		return fmt.Errorf("open runtime manifest directory: %w", err)
	}
	if err := ops.sync(directory); err != nil {
		_ = ops.close(directory)
		return fmt.Errorf("sync runtime manifest directory: %w", err)
	}
	if err := ops.close(directory); err != nil {
		return fmt.Errorf("close runtime manifest directory: %w", err)
	}
	return nil
}

// ReadRuntimeManifest reads and validates one complete runtime manifest.
func ReadRuntimeManifest(path string) (RuntimeManifest, error) {
	if err := validateManifestPath(path); err != nil {
		return RuntimeManifest{}, err
	}
	// #nosec G304 -- validateManifestPath rejected noncanonical and symlinked paths.
	contents, err := os.ReadFile(path)
	if err != nil {
		return RuntimeManifest{}, fmt.Errorf("read runtime manifest: %w", err)
	}
	var manifest RuntimeManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return RuntimeManifest{}, fmt.Errorf("decode runtime manifest: %w", err)
	}
	if err := validateRuntimeManifest(path, manifest); err != nil {
		return RuntimeManifest{}, err
	}
	return manifest, nil
}

func validateRuntimeManifest(path string, manifest RuntimeManifest) error {
	if manifest.SchemaVersion != runtimeManifestSchemaVersion {
		return fmt.Errorf("unsupported runtime manifest schema %d", manifest.SchemaVersion)
	}
	if err := manifest.Identity.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.ReservationID) == "" || strings.TrimSpace(manifest.LeaseID) == "" {
		return errors.New("runtime manifest reservation_id and lease_id are required")
	}
	if err := validateManifestPath(path); err != nil {
		return err
	}
	if manifest.ManifestPath != path {
		return errors.New("runtime manifest path does not match destination")
	}
	evidenceRoot, err := validateManagedRoots(filepath.Dir(path), manifest.Roots)
	if err != nil {
		return err
	}
	if !knownManifestState(manifest.State) {
		return fmt.Errorf("unknown runtime manifest state %q", manifest.State)
	}
	return validateManifestEvidence(filepath.Dir(path), evidenceRoot, manifest)
}

func validateManifestPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return fmt.Errorf("runtime manifest path is not canonical: %q", path)
	}
	return rejectSymlinkComponents(filepath.Dir(path), path)
}

func validateManagedRoots(base string, roots []ManagedRoot) (string, error) {
	seen := make(map[string]struct{}, len(roots))
	tempCount, evidenceCount := 0, 0
	evidenceRoot := ""
	for _, root := range roots {
		if err := validateManagedRoot(base, root); err != nil {
			return "", err
		}
		if _, exists := seen[root.Path]; exists {
			return "", fmt.Errorf("duplicate managed root: %s", root.Path)
		}
		seen[root.Path] = struct{}{}
		switch root.Class {
		case RootCache:
		case RootTemp:
			tempCount++
		case RootEvidence:
			evidenceCount++
			evidenceRoot = root.Path
		}
	}
	if tempCount == 0 || evidenceCount != 1 {
		return "", errors.New("runtime manifest requires a disposable temp root and exactly one durable evidence root")
	}
	return evidenceRoot, nil
}

func validateManagedRoot(base string, root ManagedRoot) error {
	if !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || filepath.Dir(root.Path) == root.Path {
		return fmt.Errorf("managed root is not canonical: %q", root.Path)
	}
	if err := rejectSymlinkComponents(base, root.Path); err != nil {
		return err
	}
	switch root.Class {
	case RootCache:
		if root.Disposition != RootDisposable && root.Disposition != RootShared {
			return errors.New("cache root must be disposable or shared")
		}
	case RootTemp:
		if root.Disposition != RootDisposable {
			return errors.New("temp root must be disposable")
		}
	case RootEvidence:
		if root.Disposition != RootDurable {
			return errors.New("evidence root must be durable")
		}
	default:
		return fmt.Errorf("unknown managed root class %q", root.Class)
	}
	return nil
}

func validateManifestEvidence(base, evidenceRoot string, manifest RuntimeManifest) error {
	hasPath, hasHash := manifest.EvidencePath != "", manifest.EvidenceSHA256 != ""
	if hasPath != hasHash {
		return errors.New("runtime manifest evidence path and hash must be set together")
	}
	required := manifest.State == ManifestFinalizing || manifest.State == ManifestFinalized || manifest.State == ManifestReclaimable
	if required && !hasPath {
		return fmt.Errorf("runtime manifest state %s requires evidence", manifest.State)
	}
	if !hasPath {
		return nil
	}
	if !lowercaseSHA256.MatchString(manifest.EvidenceSHA256) {
		return errors.New("runtime manifest evidence hash must be lowercase SHA-256")
	}
	if !filepath.IsAbs(manifest.EvidencePath) || filepath.Clean(manifest.EvidencePath) != manifest.EvidencePath {
		return errors.New("runtime manifest evidence path is not canonical")
	}
	relative, err := filepath.Rel(evidenceRoot, manifest.EvidencePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("runtime manifest evidence path is outside durable root")
	}
	return rejectSymlinkComponents(base, manifest.EvidencePath)
}

func knownManifestState(state ManifestState) bool {
	switch state {
	case ManifestAllocating, ManifestActive, ManifestFinalizing, ManifestFinalized, ManifestInterrupted, ManifestReclaimable:
		return true
	default:
		return false
	}
}

func validManifestTransition(from, to ManifestState) bool {
	switch from {
	case ManifestAllocating:
		return to == ManifestActive
	case ManifestActive:
		return to == ManifestFinalizing || to == ManifestInterrupted
	case ManifestFinalizing:
		return to == ManifestFinalized || to == ManifestInterrupted
	case ManifestFinalized:
		return to == ManifestReclaimable
	default:
		return false
	}
}

func sameRuntimeManifestIdentity(left, right RuntimeManifest) bool {
	return left.SchemaVersion == right.SchemaVersion &&
		left.Identity == right.Identity &&
		left.ReservationID == right.ReservationID &&
		left.LeaseID == right.LeaseID &&
		left.ManifestPath == right.ManifestPath &&
		reflect.DeepEqual(left.Roots, right.Roots)
}

func rejectSymlinkComponents(_, candidate string) error {
	volume := filepath.VolumeName(candidate)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(candidate, current)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect path component %q: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symlink", current)
		}
	}
	return nil
}
