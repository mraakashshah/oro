package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

type managedOracleHookErrorKind string

const (
	managedOracleHookMissing       managedOracleHookErrorKind = "missing"
	managedOracleHookCanonicalize  managedOracleHookErrorKind = "canonicalization"
	managedOracleHookSymlink       managedOracleHookErrorKind = "symlink"
	managedOracleHookNotRegular    managedOracleHookErrorKind = "not_regular"
	managedOracleHookNotExecutable managedOracleHookErrorKind = "not_executable"
	managedOracleHookWrongOwner    managedOracleHookErrorKind = "wrong_owner"
	managedOracleHookGroupWritable managedOracleHookErrorKind = "group_writable"
	managedOracleHookWorldWritable managedOracleHookErrorKind = "world_writable"
)

// ManagedOracleHookError identifies an unsafe managed Oracle hook file.
type ManagedOracleHookError struct {
	Kind managedOracleHookErrorKind
	Path string
	Err  error
}

func (e *ManagedOracleHookError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("managed Oracle hook %s %q: %v", e.Kind, e.Path, e.Err)
	}
	return fmt.Sprintf("managed Oracle hook %s: %q", e.Kind, e.Path)
}

func (e *ManagedOracleHookError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type managedHookFileFacts struct {
	Mode    fs.FileMode
	UID     uint32
	Symlink bool
}

// ValidateManagedOracleHook returns the canonical absolute hook path only when
// the file is regular, executable, owned by this user, and not writable by its
// group or by other users.
func ValidateManagedOracleHook(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", managedOracleHookError(managedOracleHookCanonicalize, path, err)
	}

	info, err := os.Lstat(absPath)
	if err != nil {
		kind := managedOracleHookCanonicalize
		if errors.Is(err, fs.ErrNotExist) {
			kind = managedOracleHookMissing
		}
		return "", managedOracleHookError(kind, absPath, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", managedOracleHookError(managedOracleHookSymlink, absPath, nil)
	}

	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", managedOracleHookError(managedOracleHookCanonicalize, absPath, err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", managedOracleHookError(managedOracleHookCanonicalize, canonicalPath, errors.New("file ownership is unavailable"))
	}
	if err := validateManagedOracleHookFacts(managedHookFileFacts{
		Mode:    info.Mode(),
		UID:     stat.Uid,
		Symlink: info.Mode()&fs.ModeSymlink != 0,
	}, uint32(os.Getuid())); err != nil {
		var hookErr *ManagedOracleHookError
		if errors.As(err, &hookErr) {
			hookErr.Path = canonicalPath
		}
		return "", err
	}
	return canonicalPath, nil
}

func validateManagedOracleHookFacts(f managedHookFileFacts, currentUID uint32) error {
	if f.Symlink || f.Mode&fs.ModeSymlink != 0 {
		return managedOracleHookError(managedOracleHookSymlink, "", nil)
	}
	if !f.Mode.IsRegular() {
		return managedOracleHookError(managedOracleHookNotRegular, "", nil)
	}
	if f.Mode.Perm()&0o111 == 0 {
		return managedOracleHookError(managedOracleHookNotExecutable, "", nil)
	}
	if f.UID != currentUID {
		return managedOracleHookError(managedOracleHookWrongOwner, "", nil)
	}
	if f.Mode.Perm()&0o020 != 0 {
		return managedOracleHookError(managedOracleHookGroupWritable, "", nil)
	}
	if f.Mode.Perm()&0o002 != 0 {
		return managedOracleHookError(managedOracleHookWorldWritable, "", nil)
	}
	return nil
}

func managedOracleHookError(kind managedOracleHookErrorKind, path string, err error) error {
	return &ManagedOracleHookError{Kind: kind, Path: path, Err: err}
}
