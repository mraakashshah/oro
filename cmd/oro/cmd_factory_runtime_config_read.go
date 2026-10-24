package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// readFactoryRuntimeConfig performs no publication, initialization, execution or
// authority grant. The caller supplies the digest from its trusted configuration
// and must compare the returned identities to its verified admission and permit.
func readFactoryRuntimeConfig(ctx context.Context, path, digest string) (factoryDockerCleanupConfig, error) {
	var result factoryDockerCleanupConfig
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("read factory runtime config canceled: %w", err)
	}
	if !factoryJobLowerHex(digest, 64) {
		return result, errors.New("factory runtime config requires an admitted SHA-256")
	}
	root, err := openFactoryRuntimeBundle(path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	data, err := readFactoryRuntimeFile(root, "runtime.json", factoryRuntimeConfigMaxBytes)
	if err != nil {
		return result, err
	}
	if factoryHostDigest(data) != digest {
		return result, errors.New("factory runtime config differs from its admitted digest")
	}
	config, err := decodeFactoryRuntimeConfig(data)
	if err != nil {
		return result, err
	}
	if path != factoryRuntimeConfigPath(config) {
		return result, errors.New("factory runtime config is outside its exact objective path")
	}
	if err := verifyFactoryRuntimeConfig(ctx, config); err != nil {
		return result, err
	}
	if err := verifyFactoryRuntimeSnapshot(root, config); err != nil {
		return result, err
	}
	if err := factoryRuntimeDirectoryEntries(root, []string{"docker", "runtime.json"}); err != nil {
		return result, err
	}
	if err := bindFactoryRuntimeRoot(root, filepath.Dir(path)); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("read factory runtime config canceled: %w", err)
	}
	return factoryDockerCleanupConfig{
		factoryRuntimeConfig: config, RuntimeConfigPath: path,
		RuntimeConfigSHA256: digest, DockerConfig: filepath.Join(filepath.Dir(path), "docker"),
	}, nil
}

func decodeFactoryRuntimeConfig(data []byte) (factoryRuntimeConfig, error) {
	var config factoryRuntimeConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode factory runtime config: %w", err)
	}
	canonical, err := canonicalFactoryRuntimeConfig(config)
	if err != nil {
		return config, err
	}
	if !bytes.Equal(canonical, data) {
		return config, errors.New("factory runtime config is not exact canonical JSON")
	}
	return config, nil
}

func openFactoryRuntimeBundle(path string) (*os.Root, error) {
	if !factoryJobAbsolutePath(path) || filepath.Base(path) != "runtime.json" || filepath.Base(filepath.Dir(path)) != "runtime" {
		return nil, errors.New("factory runtime config requires its fixed canonical artifact path")
	}
	objectiveDir := filepath.Dir(filepath.Dir(path))
	stateDir := filepath.Dir(objectiveDir)
	if err := factoryHostPrivateDirectory(stateDir); err != nil {
		return nil, err
	}
	state, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, fmt.Errorf("open factory runtime state root: %w", err)
	}
	defer state.Close()
	if err := bindFactoryRuntimeRoot(state, stateDir); err != nil {
		return nil, err
	}
	objective, err := openFactoryRuntimeDirectory(state, filepath.Base(objectiveDir))
	if err != nil {
		return nil, err
	}
	defer objective.Close()
	return openFactoryRuntimeDirectory(objective, "runtime")
}

func bindFactoryRuntimeRoot(root *os.Root, path string) error {
	if err := factoryHostPrivateDirectory(path); err != nil {
		return err
	}
	entry, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect factory runtime directory path: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !entry.IsDir() || !os.SameFile(entry, opened) {
		return errors.New("factory runtime directory identity changed")
	}
	return nil
}

func openFactoryRuntimeDirectory(parent *os.Root, name string) (*os.Root, error) {
	entry, err := parent.Lstat(name)
	if err != nil || !entry.IsDir() || !factoryHostPrivateOwner(entry) || entry.Mode().Perm() != 0o700 {
		return nil, errors.New("factory runtime directory must be owned, private and not an alias")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open factory runtime directory: %w", err)
	}
	opened, openErr := root.Stat(".")
	after, afterErr := parent.Lstat(name)
	if openErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(entry, opened) || !os.SameFile(opened, after) {
		_ = root.Close()
		return nil, errors.New("factory runtime directory path changed")
	}
	return root, nil
}

func readFactoryRuntimeFile(root *os.Root, name string, limit int64) ([]byte, error) {
	// os.Root may follow contained links even with O_NOFOLLOW. Bind both leaf
	// Lstats to the opened inode, and reject aliases before opening anything.
	entry, err := root.Lstat(name)
	if err != nil || !factoryRuntimeFileInfo(entry, limit) {
		return nil, errors.New("factory runtime file must be an immutable owned bounded regular inode")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open factory runtime file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !factoryRuntimeFileInfo(opened, limit) || !os.SameFile(entry, opened) {
		return nil, errors.New("factory runtime file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read factory runtime file: %w", err)
	}
	after, err := root.Lstat(name)
	if err != nil || !factoryRuntimeFileInfo(after, limit) || !os.SameFile(opened, after) ||
		int64(len(data)) != opened.Size() || int64(len(data)) != after.Size() || int64(len(data)) > limit {
		return nil, errors.New("factory runtime file changed while reading")
	}
	return data, nil
}

func factoryRuntimeFileInfo(info os.FileInfo, limit int64) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0o400 && stat.Uid == uint32(os.Getuid()) &&
		stat.Nlink == 1 && info.Size() >= 0 && info.Size() <= limit
}

func verifyFactoryRuntimeSnapshot(root *os.Root, config factoryRuntimeConfig) error {
	cli, metadata, err := factoryDockerContextSnapshot(config)
	if err != nil {
		return err
	}
	if err := factoryRuntimeDirectoryEntries(root, []string{"docker", "runtime.json"}); err != nil {
		return err
	}
	return verifyFactoryRuntimeSnapshotDirectory(root, "docker", []string{"config.json", "contexts"}, func(docker *os.Root) error {
		data, err := readFactoryRuntimeFile(docker, "config.json", 2)
		if err != nil || !bytes.Equal(data, cli) {
			return errors.New("factory Docker config must contain exactly {}")
		}
		return verifyFactoryRuntimeSnapshotDirectory(docker, "contexts", []string{"meta"}, func(contexts *os.Root) error {
			name := factoryHostDigest([]byte(config.DockerContext))
			return verifyFactoryRuntimeSnapshotDirectory(contexts, "meta", []string{name}, func(meta *os.Root) error {
				return verifyFactoryRuntimeSnapshotDirectory(meta, name, []string{"meta.json"}, func(context *os.Root) error {
					data, err := readFactoryRuntimeFile(context, "meta.json", factoryRuntimeContextMaxBytes)
					if err != nil || !bytes.Equal(data, metadata) {
						return errors.New("factory Docker context differs from its exact admitted snapshot")
					}
					return nil
				})
			})
		})
	})
}

func verifyFactoryRuntimeSnapshotDirectory(parent *os.Root, name string, entries []string, verify func(*os.Root) error) error {
	root, err := openFactoryRuntimeDirectory(parent, name)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := factoryRuntimeDirectoryEntries(root, entries); err != nil {
		return err
	}
	if err := verify(root); err != nil {
		return err
	}
	entry, err := parent.Lstat(name)
	opened, openErr := root.Stat(".")
	if err != nil || openErr != nil || !entry.IsDir() || !factoryHostPrivateOwner(entry) || !os.SameFile(entry, opened) {
		return errors.New("factory runtime snapshot directory identity changed")
	}
	return factoryRuntimeDirectoryEntries(root, entries)
}

func factoryRuntimeDirectoryEntries(root *os.Root, expected []string) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open factory runtime directory entries: %w", err)
	}
	defer directory.Close()
	// An extra entry is enough to reject; never allocate for an unbounded tree.
	names, err := directory.Readdirnames(len(expected) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read factory runtime directory entries: %w", err)
	}
	slices.Sort(names)
	if !slices.Equal(names, expected) {
		return errors.New("factory runtime snapshot contains missing or extra entries")
	}
	return nil
}

func verifyFactoryRuntimeConfig(ctx context.Context, config factoryRuntimeConfig) error {
	canonical, err := filepath.EvalSymlinks(config.Repository)
	if err != nil || canonical != config.Repository {
		return errors.New("factory runtime repository is not canonical")
	}
	info, err := os.Lstat(config.Repository)
	if err != nil || !info.IsDir() {
		return errors.New("factory runtime repository must be a directory")
	}
	for _, program := range []struct{ path, digest string }{
		{config.OroPath, config.OroSHA256}, {config.GitPath, config.GitSHA256}, {config.DockerPath, config.DockerSHA256},
	} {
		if err := verifyFactoryRuntimeUniqueProgram(ctx, program.path, program.digest); err != nil {
			return err
		}
	}
	return verifyFactoryRuntimeEndpoint(config.DockerEndpoint)
}

func verifyFactoryRuntimeUniqueProgram(ctx context.Context, path, digest string) error {
	entry, err := os.Lstat(path)
	if err != nil || !factoryRuntimeUniqueProgramInfo(entry) {
		return errors.New("factory runtime program cannot be an alias or special file")
	}
	if err := verifyFactoryRuntimeProgram(ctx, path, digest); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil || !factoryRuntimeUniqueProgramInfo(after) || !os.SameFile(entry, after) {
		return errors.New("factory runtime program identity changed")
	}
	return nil
}

func factoryRuntimeUniqueProgramInfo(info os.FileInfo) bool {
	if info == nil || !factoryRuntimeProgramInfo(info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func verifyFactoryRuntimeEndpoint(endpoint string) error {
	path := strings.TrimPrefix(endpoint, "unix://")
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("factory Docker endpoint must exist without aliases")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || !factoryHostPrivateOwner(info) {
		return errors.New("factory Docker endpoint must be an owned private Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("factory Docker endpoint cannot have hardlink aliases")
	}
	return nil
}
