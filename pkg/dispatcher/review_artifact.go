package dispatcher

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"oro/pkg/protocol"
	"oro/pkg/reviewcontract"
)

const (
	maxReviewRecoveryInlineBytes   = 192 * 1024
	maxReviewRecoveryArtifactBytes = 64 * 1024 * 1024
	recoveryArtifactFileMode       = 0o600
	recoveryArtifactDirMode        = 0o700
)

var reviewRecoveryArtifactLifecycleMu sync.RWMutex //nolint:gochecknoglobals // serializes process-wide artifact writes, commits, loads, and pruning.

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
// The staged recovery transport is ASSIGN-only: findings are never copied into
// dispatcher event payloads, so the event-payload clause is intentionally not
// part of this helper's wire budget.
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
	reviewRecoveryArtifactLifecycleMu.Lock()
	defer reviewRecoveryArtifactLifecycleMu.Unlock()
	return persistRecoveryArtifactUnlocked(dir, checkpointID, findings)
}

func persistRecoveryArtifactUnlocked(
	dir string,
	checkpointID int64,
	findings []reviewcontract.Finding,
) (ReviewRecoveryArtifactRef, error) {
	return persistRecoveryArtifactWithDirSync(dir, checkpointID, findings, syncRecoveryArtifactDirectory)
}

func persistRecoveryArtifactWithDirSync(
	dir string,
	checkpointID int64,
	findings []reviewcontract.Finding,
	syncDirectory func(*os.File) error,
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
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	path := filepath.Join(dir, fmt.Sprintf("checkpoint-%d-%s.json", checkpointID, sha))
	if err := writeRecoveryArtifactAtomically(dir, filepath.Base(path), data, syncDirectory); err != nil {
		return ReviewRecoveryArtifactRef{}, err
	}
	return ReviewRecoveryArtifactRef{
		Path:         path,
		SHA256:       sha,
		Bytes:        int64(len(data)),
		FindingCount: len(findings),
	}, nil
}

func writeRecoveryArtifactAtomically(dir, name string, data []byte, syncDirectory func(*os.File) error) error {
	directory, err := openSecuredRecoveryArtifactDir(dir, syncDirectory)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()

	temporary, temporaryName, err := createRecoveryArtifactTemporary(directory)
	if err != nil {
		return err
	}
	defer func() {
		_ = temporary.Close()
		_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
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
	if err := unix.Renameat(int(directory.Fd()), temporaryName, int(directory.Fd()), name); err != nil {
		return fmt.Errorf("commit recovery artifact: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync recovery artifact directory: %w", err)
	}
	return nil
}

func openSecuredRecoveryArtifactDir(dir string, syncDirectory func(*os.File) error) (*os.File, error) {
	canonicalAnchor, components, err := recoveryArtifactPathComponents(dir)
	if err != nil {
		return nil, err
	}

	directory, err := os.Open(canonicalAnchor) //nolint:gosec // the anchor is canonicalized before descriptor-relative traversal.
	if err != nil {
		return nil, fmt.Errorf("open recovery artifact directory anchor: %w", err)
	}
	for index, component := range components {
		next, err := openRecoveryArtifactDirectoryComponent(directory, component, syncDirectory)
		if err != nil {
			return nil, err
		}
		directory = next
		if index == len(components)-1 {
			if err := directory.Chmod(recoveryArtifactDirMode); err != nil {
				_ = directory.Close()
				return nil, fmt.Errorf("secure recovery artifact directory: %w", err)
			}
		}
	}
	return directory, nil
}

func recoveryArtifactPathComponents(dir string) (canonicalAnchor string, components []string, err error) {
	return confinedRecoveryArtifactPathComponents(dir, 2)
}

func confinedRecoveryArtifactPathComponents(
	path string,
	protectedComponents int,
) (canonicalAnchor string, components []string, err error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve recovery artifact path: %w", err)
	}
	anchor := absolute
	for range protectedComponents {
		parent := filepath.Dir(anchor)
		if parent == anchor {
			break
		}
		anchor = parent
	}
	canonicalAnchor, err = filepath.EvalSymlinks(anchor)
	if err != nil {
		return "", nil, fmt.Errorf("resolve recovery artifact path anchor: %w", err)
	}
	relative, err := filepath.Rel(anchor, absolute)
	if err != nil {
		return "", nil, fmt.Errorf("resolve recovery artifact path components: %w", err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("resolve recovery artifact path: unsafe path")
	}
	return canonicalAnchor, strings.Split(relative, string(filepath.Separator)), nil
}

func openExistingRecoveryArtifact(path string) (*os.File, error) {
	canonicalAnchor, components, err := confinedRecoveryArtifactPathComponents(path, 3)
	if err != nil {
		return nil, err
	}
	return openExistingRecoveryArtifactComponents(canonicalAnchor, components, false)
}

func openExistingRecoveryArtifactDirectory(path string) (*os.File, error) {
	canonicalAnchor, components, err := confinedRecoveryArtifactPathComponents(path, 2)
	if err != nil {
		return nil, err
	}
	return openExistingRecoveryArtifactComponents(canonicalAnchor, components, true)
}

func openExistingRecoveryArtifactComponents(
	canonicalAnchor string,
	components []string,
	finalDirectory bool,
) (*os.File, error) {
	current, err := os.Open(canonicalAnchor) //nolint:gosec // canonical trusted anchor for descriptor-relative traversal.
	if err != nil {
		return nil, fmt.Errorf("open recovery artifact path anchor: %w", err)
	}
	for index, component := range components {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(components)-1 || finalDirectory {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		nextFD, err := unix.Openat(int(current.Fd()), component, flags, 0)
		if err != nil {
			_ = current.Close()
			return nil, fmt.Errorf("open recovery artifact path component %q without following symlinks: %w", component, err)
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), component))
		if next == nil {
			_ = unix.Close(nextFD)
			_ = current.Close()
			return nil, fmt.Errorf("open recovery artifact path component %q: invalid file descriptor", component)
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func openRecoveryArtifactAt(directory *os.File, name string) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(int(directory.Fd()), name, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open recovery artifact candidate %q without following symlinks: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.Name(), name))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open recovery artifact candidate %q: invalid file descriptor", name)
	}
	return file, nil
}

func removeRecoveryArtifactConfined(path string) error {
	canonicalAnchor, components, err := confinedRecoveryArtifactPathComponents(path, 3)
	if err != nil {
		return err
	}
	if len(components) == 0 {
		return errors.New("remove recovery artifact: path has no file component")
	}
	parent, err := openExistingRecoveryArtifactComponents(canonicalAnchor, components[:len(components)-1], true)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := unlinkRecoveryArtifactAt(parent, components[len(components)-1]); err != nil {
		return fmt.Errorf("remove recovery artifact without following symlinks: %w", err)
	}
	if err := syncRecoveryArtifactDirectory(parent); err != nil {
		return fmt.Errorf("sync recovery artifact deletion: %w", err)
	}
	return nil
}

func unlinkRecoveryArtifactAt(parent *os.File, name string) error {
	err := unix.Unlinkat(int(parent.Fd()), name, 0)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EISDIR) || errors.Is(err, unix.EPERM) {
		if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("unlink recovery artifact directory: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unlink recovery artifact file: %w", err)
}

func openRecoveryArtifactDirectoryComponent(
	directory *os.File,
	component string,
	syncDirectory func(*os.File) error,
) (*os.File, error) {
	if err := unix.Mkdirat(int(directory.Fd()), component, recoveryArtifactDirMode); err != nil && !errors.Is(err, unix.EEXIST) {
		_ = directory.Close()
		return nil, fmt.Errorf("create recovery artifact directory component %q: %w", component, err)
	}
	if err := syncDirectory(directory); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("sync parent of recovery artifact directory component %q: %w", component, err)
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	nextFD, err := unix.Openat(int(directory.Fd()), component, flags, 0)
	if err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("open recovery artifact directory component %q without following symlinks: %w", component, err)
	}
	next := os.NewFile(uintptr(nextFD), filepath.Join(directory.Name(), component))
	if next == nil {
		_ = unix.Close(nextFD)
		_ = directory.Close()
		return nil, fmt.Errorf("open recovery artifact directory component %q: invalid file descriptor", component)
	}
	_ = directory.Close()
	return next, nil
}

func syncRecoveryArtifactDirectory(directory *os.File) error {
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func createRecoveryArtifactTemporary(directory *os.File) (*os.File, string, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate recovery artifact temporary name: %w", err)
		}
		name := ".review-recovery-" + hex.EncodeToString(random[:]) + ".tmp"
		flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
		fd, err := unix.Openat(int(directory.Fd()), name, flags, recoveryArtifactFileMode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create recovery artifact temporary: %w", err)
		}
		file := os.NewFile(uintptr(fd), filepath.Join(directory.Name(), name))
		if file == nil {
			_ = unix.Close(fd)
			return nil, "", errors.New("create recovery artifact temporary: invalid file descriptor")
		}
		return file, name, nil
	}
	return nil, "", errors.New("create recovery artifact temporary: exhausted unique names")
}

// LoadRecoveryArtifact verifies the committed identity before returning any findings.
func LoadRecoveryArtifact(ref ReviewRecoveryArtifactRef) ([]reviewcontract.Finding, error) {
	reviewRecoveryArtifactLifecycleMu.RLock()
	defer reviewRecoveryArtifactLifecycleMu.RUnlock()
	return loadRecoveryArtifactUnlocked(ref)
}

func loadRecoveryArtifactUnlocked(ref ReviewRecoveryArtifactRef) ([]reviewcontract.Finding, error) {
	if err := validateRecoveryArtifactRef(ref); err != nil {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, err)
	}
	file, err := openExistingRecoveryArtifact(ref.Path)
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
	if info.Size() > maxReviewRecoveryArtifactBytes {
		return nil, recoveryArtifactError(RecoveryArtifactOversized, ref.Path, fmt.Errorf("%d bytes exceeds %d-byte cap", info.Size(), maxReviewRecoveryArtifactBytes))
	}
	if info.Size() != ref.Bytes {
		return nil, recoveryArtifactError(RecoveryArtifactCorrupt, ref.Path, fmt.Errorf("byte count %d does not match %d", info.Size(), ref.Bytes))
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
