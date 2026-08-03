package dispatcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"oro/pkg/protocol"
	"oro/pkg/reviewcontract"
)

const (
	maxReviewRecoveryInlineBytes   = 192 * 1024
	maxReviewRecoveryArtifactBytes = 64 * 1024 * 1024
	recoveryArtifactFileMode       = 0o600
	recoveryArtifactDirMode        = 0o700
)

// ReviewRecoveryArtifactRef is the durable wire identity for lossless findings.
type ReviewRecoveryArtifactRef = protocol.ReviewRecoveryArtifactRef

// RecoveryArtifactErrorKind identifies a failure that requires typed checkpoint recovery.
type RecoveryArtifactErrorKind string

const (
	// RecoveryArtifactMissing means the committed artifact cannot be opened.
	RecoveryArtifactMissing RecoveryArtifactErrorKind = "missing"
	// RecoveryArtifactCorrupt means artifact bytes do not match their committed identity.
	RecoveryArtifactCorrupt RecoveryArtifactErrorKind = "corrupt"
	// RecoveryArtifactOversized means artifact bytes exceed the bounded recovery read limit.
	RecoveryArtifactOversized RecoveryArtifactErrorKind = "oversized"
)

var (
	// ErrRecoveryArtifactMissing classifies a missing committed recovery artifact.
	ErrRecoveryArtifactMissing = errors.New("recovery artifact missing")
	// ErrRecoveryArtifactCorrupt classifies an invalid or identity-mismatched recovery artifact.
	ErrRecoveryArtifactCorrupt = errors.New("recovery artifact corrupt")
	// ErrRecoveryArtifactOversized classifies an artifact that exceeds the recovery read limit.
	ErrRecoveryArtifactOversized = errors.New("recovery artifact oversized")
)

// RecoveryArtifactError prevents callers from degrading artifact failures into partial findings.
type RecoveryArtifactError struct {
	Kind RecoveryArtifactErrorKind
	Path string
	Err  error
}

func (e *RecoveryArtifactError) Error() string {
	if e == nil {
		return "recovery artifact error"
	}
	if e.Err == nil {
		return fmt.Sprintf("recovery artifact %s: %s", e.Kind, e.Path)
	}
	return fmt.Sprintf("recovery artifact %s %s: %v", e.Kind, e.Path, e.Err)
}

// Unwrap preserves the underlying filesystem or decoding error.
func (e *RecoveryArtifactError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is supports stable sentinel classification in addition to errors.As.
func (e *RecoveryArtifactError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrRecoveryArtifactMissing:
		return e.Kind == RecoveryArtifactMissing
	case ErrRecoveryArtifactCorrupt:
		return e.Kind == RecoveryArtifactCorrupt
	case ErrRecoveryArtifactOversized:
		return e.Kind == RecoveryArtifactOversized
	default:
		return false
	}
}

// prepareReviewRecovery keeps bounded findings inline and persists larger findings by reference.
func prepareReviewRecovery(
	dir string,
	checkpointID int64,
	rejectedHeadSHA string,
	acceptanceHash string,
	attempt int,
	findings []reviewcontract.Finding,
) (protocol.ReviewRecovery, error) {
	recovery := protocol.ReviewRecovery{
		CheckpointID:    checkpointID,
		RejectedHeadSHA: rejectedHeadSHA,
		Findings:        findings,
		Attempt:         attempt,
		AcceptanceHash:  acceptanceHash,
	}
	if checkpointID <= 0 || rejectedHeadSHA == "" || acceptanceHash == "" || attempt < 0 {
		return protocol.ReviewRecovery{}, errors.New("prepare review recovery: missing required identity")
	}
	encoded, err := json.Marshal(recovery)
	if err != nil {
		return protocol.ReviewRecovery{}, fmt.Errorf("marshal inline review recovery: %w", err)
	}
	if len(encoded) <= maxReviewRecoveryInlineBytes {
		return recovery, nil
	}

	ref, err := PersistRecoveryArtifact(dir, checkpointID, findings)
	if err != nil {
		return protocol.ReviewRecovery{}, err
	}
	recovery.Findings = nil
	recovery.FindingsRef = &ref
	encoded, err = json.Marshal(recovery)
	if err != nil {
		return protocol.ReviewRecovery{}, fmt.Errorf("marshal referenced review recovery: %w", err)
	}
	if len(encoded) > maxReviewRecoveryInlineBytes {
		return protocol.ReviewRecovery{}, fmt.Errorf("referenced review recovery is %d bytes, exceeds %d-byte cap", len(encoded), maxReviewRecoveryInlineBytes)
	}
	return recovery, nil
}

// PersistRecoveryArtifact atomically stores exact findings and returns their content identity.
func PersistRecoveryArtifact(
	dir string,
	checkpointID int64,
	findings []reviewcontract.Finding,
) (ReviewRecoveryArtifactRef, error) {
	if dir == "" {
		return ReviewRecoveryArtifactRef{}, errors.New("persist recovery artifact: directory is empty")
	}
	if checkpointID <= 0 {
		return ReviewRecoveryArtifactRef{}, fmt.Errorf("persist recovery artifact: invalid checkpoint ID %d", checkpointID)
	}
	data, err := json.Marshal(findings)
	if err != nil {
		return ReviewRecoveryArtifactRef{}, fmt.Errorf("marshal recovery artifact: %w", err)
	}
	if len(data) > maxReviewRecoveryArtifactBytes {
		return ReviewRecoveryArtifactRef{}, &RecoveryArtifactError{
			Kind: RecoveryArtifactOversized,
			Path: dir,
			Err:  fmt.Errorf("%d bytes exceeds %d-byte cap", len(data), maxReviewRecoveryArtifactBytes),
		}
	}
	if err := os.MkdirAll(dir, recoveryArtifactDirMode); err != nil {
		return ReviewRecoveryArtifactRef{}, fmt.Errorf("create recovery artifact directory: %w", err)
	}
	if err := os.Chmod(dir, recoveryArtifactDirMode); err != nil {
		return ReviewRecoveryArtifactRef{}, fmt.Errorf("secure recovery artifact directory: %w", err)
	}

	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	path := filepath.Join(dir, fmt.Sprintf("checkpoint-%d-%s.json", checkpointID, sha))
	if err := writeRecoveryArtifactAtomically(dir, path, data); err != nil {
		return ReviewRecoveryArtifactRef{}, err
	}
	return ReviewRecoveryArtifactRef{
		Path:         path,
		SHA256:       sha,
		Bytes:        int64(len(data)),
		FindingCount: len(findings),
	}, nil
}

func writeRecoveryArtifactAtomically(dir, path string, data []byte) error {
	temporary, err := os.CreateTemp(dir, ".review-recovery-*.tmp")
	if err != nil {
		return fmt.Errorf("create recovery artifact temporary: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(recoveryArtifactFileMode); err != nil {
		return fmt.Errorf("secure recovery artifact temporary: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write recovery artifact temporary: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync recovery artifact temporary: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close recovery artifact temporary: %w", err)
	}
	// Both paths are derived inside the caller-provided artifact directory;
	// the destination filename contains only a checkpoint integer and hex digest.
	if err := os.Rename(temporaryPath, path); err != nil { //nolint:gosec // atomic same-directory commit
		return fmt.Errorf("commit recovery artifact: %w", err)
	}
	directory, err := os.Open(dir) //nolint:gosec // dir is the caller-provided recovery directory.
	if err != nil {
		return fmt.Errorf("open recovery artifact directory for sync: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync recovery artifact directory: %w", err)
	}
	return nil
}

// LoadRecoveryArtifact verifies the committed identity before returning any findings.
func LoadRecoveryArtifact(ref ReviewRecoveryArtifactRef) ([]reviewcontract.Finding, error) {
	if err := validateRecoveryArtifactRef(ref); err != nil {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, err)
	}
	file, err := os.Open(ref.Path) //nolint:gosec // ref is a hash-verified durable checkpoint record.
	if err != nil {
		kind := RecoveryArtifactCorrupt
		if errors.Is(err, os.ErrNotExist) {
			kind = RecoveryArtifactMissing
		}
		return nil, recoveryArtifactError(kind, ref.Path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != recoveryArtifactFileMode {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, fmt.Errorf("unsafe artifact mode %s", info.Mode()))
	}
	if info.Size() != ref.Bytes {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, fmt.Errorf("byte count %d does not match %d", info.Size(), ref.Bytes))
	}
	if info.Size() > maxReviewRecoveryArtifactBytes {
		return nil, recoveryArtifactError(RecoveryArtifactOversized, ref.Path, fmt.Errorf("%d bytes exceeds %d-byte cap", info.Size(), maxReviewRecoveryArtifactBytes))
	}

	data, err := io.ReadAll(io.LimitReader(file, maxReviewRecoveryArtifactBytes+1))
	if err != nil {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, err)
	}
	if len(data) > maxReviewRecoveryArtifactBytes {
		return nil, recoveryArtifactError(RecoveryArtifactOversized, ref.Path, fmt.Errorf("artifact exceeds %d-byte cap", maxReviewRecoveryArtifactBytes))
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != ref.SHA256 {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, errors.New("SHA-256 mismatch"))
	}
	var findings []reviewcontract.Finding
	if err := json.Unmarshal(data, &findings); err != nil {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, fmt.Errorf("decode findings: %w", err))
	}
	if len(findings) != ref.FindingCount {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, fmt.Errorf("finding count %d does not match %d", len(findings), ref.FindingCount))
	}
	return findings, nil
}

func validateRecoveryArtifactRef(ref ReviewRecoveryArtifactRef) error {
	if ref.Path == "" || ref.Bytes <= 0 || ref.FindingCount < 0 {
		return errors.New("incomplete recovery artifact reference")
	}
	digest, err := hex.DecodeString(ref.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("invalid recovery artifact SHA-256")
	}
	return nil
}

func recoveryArtifactError(kind RecoveryArtifactErrorKind, path string, err error) *RecoveryArtifactError {
	return &RecoveryArtifactError{Kind: kind, Path: path, Err: err}
}
