package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const capabilityFileMode os.FileMode = 0o600

// AssignmentCredential is the short-lived assignment authority read by Oro
// commands launched from an already-running agent.
type AssignmentCredential struct {
	AssignmentID int64     `json:"assignment_id"`
	Generation   int64     `json:"generation"`
	CapabilityID string    `json:"capability_id"`
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ReplaceCapabilityFile atomically installs credential at path with mode 0600.
// It writes and verifies a same-directory temporary before renaming it, so a
// failed write leaves the previous credential readable.
func ReplaceCapabilityFile(path string, credential AssignmentCredential) error {
	if path == "" {
		return errors.New("capability file path is empty")
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("marshal assignment credential: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create capability directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".capability-*")
	if err != nil {
		return fmt.Errorf("create capability temporary: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		// The temporary is created exclusively above and is safe to remove on
		// any pre-rename error; Rename has already moved it on success.
		_ = os.Remove(temporaryPath) //nolint:gosec // path is from os.CreateTemp
	}()
	if err := temporary.Chmod(capabilityFileMode); err != nil {
		return fmt.Errorf("set capability file mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write capability file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync capability file: %w", err)
	}
	if err := verifyCapabilityFileMode(temporary); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close capability file: %w", err)
	}
	//nolint:gosec // temporaryPath comes from os.CreateTemp in the destination directory.
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace capability file: %w", err)
	}
	return nil
}

// ReadCapabilityFile rereads and validates the live assignment credential.
func ReadCapabilityFile(path string) (AssignmentCredential, error) {
	file, err := os.Open(path) //nolint:gosec // caller supplies the configured capability file path
	if err != nil {
		return AssignmentCredential{}, fmt.Errorf("open capability file: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := verifyCapabilityFileMode(file); err != nil {
		return AssignmentCredential{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return AssignmentCredential{}, fmt.Errorf("read capability file: %w", err)
	}
	var credential AssignmentCredential
	if err := json.Unmarshal(data, &credential); err != nil {
		return AssignmentCredential{}, fmt.Errorf("decode capability file: %w", err)
	}
	return credential, nil
}

// RemoveCapabilityFile revokes the local credential when an assignment ends.
func RemoveCapabilityFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove capability file: %w", err)
	}
	return nil
}

func verifyCapabilityFileMode(file interface{ Stat() (os.FileInfo, error) }) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat capability file: %w", err)
	}
	if info.Mode().Perm() != capabilityFileMode {
		return fmt.Errorf("unsafe capability file mode %o", info.Mode().Perm())
	}
	return nil
}
