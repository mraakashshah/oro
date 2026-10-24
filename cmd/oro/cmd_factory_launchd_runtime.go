package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"

	"oro/pkg/factory"
)

type factoryLaunchdConfig struct {
	StateRoot, ObjectiveID, OroPath, OroSHA256 string
	GitPath, GitSHA256, Repository, TargetRef  string
	RuntimeConfigPath, RuntimeConfigSHA256     string
	CleanupTimeout                             time.Duration
	HardStop                                   time.Time
}

// Config is populated only by trusted CLI resume wiring from verified admission
// artifacts. ScopeAuthority supplies ownership, not executable or recovery policy.
func validateFactoryLaunchdConfig(ctx context.Context, config factoryLaunchdConfig) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(config.ObjectiveID) ||
		config.CleanupTimeout <= 0 || config.HardStop.IsZero() || config.HardStop.UTC().Year() > 9999 {
		return errors.New("invalid factory runtime objective or frozen recovery bounds")
	}
	if !factoryJobAbsolutePath(config.StateRoot) {
		return errors.New("factory runtime requires a canonical state root")
	}
	if err := validateFactoryCleanupConfigReference(config); err != nil {
		return err
	}
	if err := factoryHostPrivateDirectory(config.StateRoot); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(config.Repository)
	if err != nil || canonical != config.Repository || !factoryJobAbsolutePath(config.Repository) {
		return errors.New("factory runtime requires a canonical repository")
	}
	if !factoryObserverRef(config.TargetRef) {
		return errors.New("factory observer requires an exact safe branch ref")
	}
	if err := verifyFactoryRuntimeProgram(ctx, config.OroPath, config.OroSHA256); err != nil {
		return err
	}
	return verifyFactoryRuntimeProgram(ctx, config.GitPath, config.GitSHA256)
}

func factoryLaunchdObjectiveHash(config factoryLaunchdConfig) string {
	digest := sha256.Sum256([]byte(config.StateRoot + "\x00" + config.ObjectiveID))
	return hex.EncodeToString(digest[:8])
}

func verifyFactoryRuntimeProgram(ctx context.Context, path, digest string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path || !factoryJobAbsolutePath(path) || !factoryJobLowerHex(digest, 64) {
		return errors.New("factory executable requires its canonical admitted path and SHA-256")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // Canonical admitted executable, never candidate input.
	if err != nil {
		return fmt.Errorf("open admitted factory executable: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect admitted factory executable: %w", err)
	}
	if !factoryRuntimeProgramInfo(info) {
		return errors.New("factory executable is not an owned bounded non-writable regular program")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify factory executable canceled: %w", err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, (512<<20)+1))
	if err != nil || n > 512<<20 || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("factory executable differs from its admitted digest")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify factory executable canceled: %w", err)
	}
	return nil
}

func factoryRuntimeProgramInfo(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 && info.Mode().Perm()&0o022 == 0 &&
		(stat.Uid == 0 || stat.Uid == uint32(os.Getuid())) && info.Size() <= 512<<20
}

func factoryObserverRef(ref string) bool {
	if !regexp.MustCompile(`^refs/heads/[a-zA-Z0-9][a-zA-Z0-9._/-]*$`).MatchString(ref) || strings.Contains(ref, "..") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func factoryObserverArgs(ref string) []string {
	return []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "--no-optional-locks", "--no-replace-objects", "show-ref", "--verify", "--hash", ref}
}

func factoryObserverEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "LC_ALL=C", "LANG=C",
	}
}

func validateFactoryObserver(ctx context.Context, config factoryLaunchdConfig, invocation factory.HostInvocation) error {
	if invocation.Path != config.GitPath || invocation.Dir != config.Repository || invocation.OutputLimit != 256 || len(invocation.Stdin) != 0 ||
		!reflect.DeepEqual(invocation.Args, factoryObserverArgs(config.TargetRef)) || !reflect.DeepEqual(invocation.Env, factoryObserverEnvironment()) {
		return errors.New("factory observer permits only the frozen read-only Git ref command")
	}
	return verifyFactoryRuntimeProgram(ctx, config.GitPath, config.GitSHA256)
}

func readFactoryRuntimeFrame(reader io.Reader, target any) error {
	var frame bytes.Buffer
	one := make([]byte, 1)
	for frame.Len() < factoryHostExecWireLimit {
		if _, err := io.ReadFull(reader, one); err != nil {
			return fmt.Errorf("receive factory runtime frame: %w", err)
		}
		if one[0] != '\n' {
			_ = frame.WriteByte(one[0])
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(frame.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("decode factory runtime frame: %w", err)
		}
		canonical, err := json.Marshal(target)
		if err != nil || !bytes.Equal(canonical, frame.Bytes()) {
			return errors.New("factory runtime frame is not canonical JSON")
		}
		return nil
	}
	return errors.New("factory runtime frame exceeds its metadata bound")
}

func receiveFactoryPublication(ctx context.Context, reader io.Reader, expected factoryHostExecPublished) (factoryHostExecPublished, error) {
	var message factoryHostExecPublished
	if err := factoryHostDeadline(ctx, expected.Deadline); err != nil {
		return message, err
	}
	if err := readFactoryRuntimeFrame(reader, &message); err != nil {
		return message, err
	}
	expected.PendingSHA256 = message.PendingSHA256
	if !message.Deadline.Equal(expected.Deadline) {
		return message, errors.New("factory PUBLISHED deadline differs from authenticated invocation")
	}
	expected.Deadline = message.Deadline
	if message != expected || !factoryJobLowerHex(message.PendingSHA256, 64) {
		return message, errors.New("factory PUBLISHED differs from authenticated invocation")
	}
	return message, factoryHostDeadline(ctx, expected.Deadline)
}

func waitFactoryGroupEmpty(ctx context.Context, pgid int, probe func(int) error) error {
	if pgid <= 1 {
		return errors.New("factory retirement requires a recorded authenticated process group")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("factory process group remains uncertain: %w", err)
		}
		if errors.Is(probe(pgid), syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}
