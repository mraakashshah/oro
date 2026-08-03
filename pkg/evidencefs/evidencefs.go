// Package evidencefs provides symlink-safe access to assignment evidence.
package evidencefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	privateDirMode  = 0o700
	privateFileMode = 0o600
)

// WriteFile atomically publishes data below root without following symlinks.
func WriteFile(root string, parents []string, name string, data []byte) error {
	dirFD, err := openEvidenceRoot(root, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	for _, parent := range parents {
		nextFD, openErr := openEvidenceDir(dirFD, parent, true)
		if openErr != nil {
			return openErr
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}
	if !safeComponent(name) {
		return errors.New("evidence filename is not a safe path component")
	}
	return writeAndPublishFile(dirFD, name, data)
}

func writeAndPublishFile(dirFD int, name string, data []byte) error {
	tmpName, err := temporaryName()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(dirFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, privateFileMode)
	if err != nil {
		return fmt.Errorf("create evidence temporary file: %w", err)
	}
	defer func() { _ = unix.Unlinkat(dirFD, tmpName, 0) }()
	file := os.NewFile(uintptr(fd), tmpName)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create evidence file handle")
	}
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure evidence temporary file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write evidence: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync evidence: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close evidence: %w", err)
	}
	if err := unix.Renameat(dirFD, tmpName, dirFD, name); err != nil {
		return fmt.Errorf("publish evidence: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync evidence directory: %w", err)
	}
	return nil
}

// ReadFile reads a regular owner-only file below root without following symlinks.
func ReadFile(root string, parents []string, name string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("evidence read limit must be positive")
	}
	dirFD, err := openEvidenceRoot(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	for _, parent := range parents {
		nextFD, openErr := openEvidenceDir(dirFD, parent, false)
		if openErr != nil {
			return nil, openErr
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}
	if !safeComponent(name) {
		return nil, errors.New("evidence filename is not a safe path component")
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open evidence file: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create evidence file handle")
	}
	defer func() { _ = file.Close() }()
	if err := requirePrivateRegularFile(fd); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read evidence: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("evidence exceeds read limit")
	}
	return data, nil
}

func openEvidenceRoot(root string, create bool) (int, error) {
	if !filepath.IsAbs(root) {
		return -1, errors.New("evidence root must be absolute")
	}
	if create {
		if err := os.MkdirAll(root, privateDirMode); err != nil {
			return -1, fmt.Errorf("create evidence root: %w", err)
		}
	}
	fd, err := unix.Open(filepath.Clean(root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open evidence root: %w", err)
	}
	if create {
		if err := unix.Fchmod(fd, privateDirMode); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("secure evidence root: %w", err)
		}
	} else if err := requirePrivateDirectory(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func openEvidenceDir(parentFD int, name string, create bool) (int, error) {
	if !safeComponent(name) {
		return -1, errors.New("evidence directory is not a safe path component")
	}
	if create {
		if err := unix.Mkdirat(parentFD, name, privateDirMode); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("create evidence directory %q: %w", name, err)
		}
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open evidence directory %q: %w", name, err)
	}
	if create {
		if err := unix.Fchmod(fd, privateDirMode); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("secure evidence directory %q: %w", name, err)
		}
	} else if err := requirePrivateDirectory(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func safeComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value
}

func temporaryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate evidence temporary filename: %w", err)
	}
	return ".qg-evidence-" + hex.EncodeToString(random[:]), nil
}

func requirePrivateDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat evidence directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o077 != 0 {
		return errors.New("evidence directory is not owner-only")
	}
	return nil
}

func requirePrivateRegularFile(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat evidence file: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
		return errors.New("evidence file is not an owner-only regular file")
	}
	return nil
}
