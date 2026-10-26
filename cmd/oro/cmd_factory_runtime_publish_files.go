package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"oro/pkg/factory"
)

func openFactoryPublication(ctx context.Context, view factory.ManagedAdmissionView, create bool) (*os.Root, *os.File, error) {
	if err := factoryHostPrivateDirectory(view.StateRoot); err != nil {
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	root, err := os.OpenRoot(view.StateRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	defer root.Close()
	if err := bindFactoryRuntimeRoot(root, view.StateRoot); err != nil {
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	objective, err := openFactoryRuntimeDirectory(root, view.ObjectiveID)
	if err != nil {
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	lock, err := openFactoryBudgetFile(objective, "runtime-publication.lock", flags, 0o600)
	if create && errors.Is(err, os.ErrExist) {
		lock, err = openFactoryBudgetFile(objective, "runtime-publication.lock", os.O_RDWR, 0)
	}
	if err != nil {
		_ = objective.Close()
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	info, err := lock.Stat()
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		_ = lock.Close()
		_ = objective.Close()
		return nil, nil, errors.New("invalid publication lock")
	}
	if err := waitFactoryBudgetLock(ctx, lock); err != nil {
		_ = lock.Close()
		_ = objective.Close()
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	if err := bindFactoryRuntimeRoot(objective, filepath.Join(view.StateRoot, view.ObjectiveID)); err != nil {
		_ = lock.Close()
		_ = objective.Close()
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	return objective, lock, nil
}

func ensureFactoryPublicationIntent(root *os.Root, want []byte) error {
	got, err := readFactoryRuntimeFile(root, "runtime-publication.json", factoryRuntimeConfigMaxBytes)
	if err == nil {
		if !bytes.Equal(got, want) {
			return errors.New("factory publication intent conflicts")
		}
		return removeFactoryPublicationStage(root, ".runtime-publication.stage", factoryRuntimeConfigMaxBytes)
	}
	if _, statErr := root.Lstat("runtime-publication.json"); !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("publication: %w", err)
	}
	for _, name := range []string{"oro", ".oro.stage", "runtime", ".runtime.stage"} {
		if _, e := root.Lstat(name); !errors.Is(e, os.ErrNotExist) {
			return errors.New("publication artifacts without intent")
		}
	}
	if err := removeFactoryPublicationStage(root, ".runtime-publication.stage", factoryRuntimeConfigMaxBytes); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := writeFactoryPublicationFile(root, ".runtime-publication.stage", want); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := root.Rename(".runtime-publication.stage", "runtime-publication.json"); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	return syncFactoryBudgetDirectory(root)
}

func removeFactoryPublicationStage(root *os.Root, name string, limit int64) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || !factoryHostPrivateOwner(info) || info.Size() > limit || (info.Mode().Perm() != 0o400 && info.Mode().Perm() != 0o600) {
		return errors.New("invalid publication staging file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("invalid publication staging inode")
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("publication removal: %w", err)
	}
	return nil
}

func writeFactoryPublicationFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := file.Chmod(0o400); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("publication sync: %w", err)
	}
	return nil
}

func ensureFactoryPublicationOro(ctx context.Context, root *os.Root, inputs factoryRuntimeInputs) error {
	info, err := root.Lstat("oro")
	if err == nil {
		if info.Mode().Perm() != 0o500 || !factoryHostPrivateOwner(info) || info.Size() <= 0 {
			return errors.New("invalid final Oro mode")
		}
		if err := verifyFactoryRuntimeUniqueProgram(ctx, filepath.Join(root.Name(), "oro"), inputs.OroSHA256); err != nil {
			return fmt.Errorf("publication: %w", err)
		}
		return removeFactoryOroStage(root)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("publication: %w", err)
	}
	if err := removeFactoryOroStage(root); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	return copyFactoryPublicationOro(ctx, root, inputs)
}

func copyFactoryPublicationOro(ctx context.Context, root *os.Root, inputs factoryRuntimeInputs) error {
	source, opened, err := openFactoryPublicationOroSource(inputs.OroPath)
	if err != nil {
		return err
	}
	defer source.Close()
	stage, err := root.OpenFile(".oro.stage", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	defer stage.Close()
	count, digest, err := copyFactoryPublicationStream(ctx, source, stage)
	if err != nil {
		return err
	}
	after, err := os.Lstat(inputs.OroPath)
	if err != nil || !os.SameFile(opened, after) || !factoryRuntimeUniqueProgramInfo(after) || after.Size() != count || digest != inputs.OroSHA256 {
		return errors.New("oro source identity changed")
	}
	if err := stage.Chmod(0o500); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := root.Rename(".oro.stage", "oro"); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	return syncFactoryBudgetDirectory(root)
}

func openFactoryPublicationOroSource(path string) (*os.File, os.FileInfo, error) {
	sourceInfo, err := os.Lstat(path)
	if err != nil || !factoryRuntimeUniqueProgramInfo(sourceInfo) || sourceInfo.Size() <= 0 || sourceInfo.Size() > 512<<20 {
		return nil, nil, errors.New("invalid Oro source")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, nil, errors.New("aliased Oro source")
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("open oro source directory: %w", err)
	}
	defer parent.Close()
	source, err := parent.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("publication: %w", err)
	}
	opened, err := source.Stat()
	if err != nil || !os.SameFile(sourceInfo, opened) {
		_ = source.Close()
		return nil, nil, errors.New("oro source changed")
	}
	return source, opened, nil
}

func copyFactoryPublicationStream(ctx context.Context, source, stage *os.File) (size int64, digest string, err error) {
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", fmt.Errorf("publication: %w", err)
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			count += int64(n)
			if count > 512<<20 {
				return 0, "", errors.New("oro source exceeds limit")
			}
			if _, err := stage.Write(buffer[:n]); err != nil {
				return 0, "", fmt.Errorf("publication: %w", err)
			}
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, "", fmt.Errorf("publication read: %w", readErr)
		}
	}
	return count, hex.EncodeToString(hash.Sum(nil)), nil
}

func removeFactoryOroStage(root *os.Root) error {
	info, err := root.Lstat(".oro.stage")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || !factoryHostPrivateOwner(info) || info.Size() > 512<<20 || (info.Mode().Perm() != 0o500 && info.Mode().Perm() != 0o600) {
		return errors.New("invalid Oro stage")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("invalid Oro stage inode")
	}
	if err := root.Remove(".oro.stage"); err != nil {
		return fmt.Errorf("publication removal: %w", err)
	}
	return nil
}

func ensureFactoryPublicationBundle(root *os.Root, config factoryRuntimeConfig, data []byte) error {
	_, err := root.Lstat("runtime")
	if err == nil {
		return verifyFactoryExistingBundle(root, config, data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("publication: %w", err)
	}
	if err := removeFactoryRuntimeStage(root, config); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	cli, meta, err := factoryDockerContextSnapshot(config)
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	paths := []string{".runtime.stage", ".runtime.stage/docker", ".runtime.stage/docker/contexts", ".runtime.stage/docker/contexts/meta", ".runtime.stage/docker/contexts/meta/" + factoryHostDigest([]byte(config.DockerContext))}
	for _, path := range paths {
		if err := root.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("publication: %w", err)
		}
	}
	files := map[string][]byte{".runtime.stage/runtime.json": data, ".runtime.stage/docker/config.json": cli, paths[len(paths)-1] + "/meta.json": meta}
	for path, contents := range files {
		if err := writeFactoryPublicationFile(root, path, contents); err != nil {
			return fmt.Errorf("publication: %w", err)
		}
	}
	for i := len(paths) - 1; i >= 0; i-- {
		dir, err := root.OpenRoot(paths[i])
		if err != nil {
			return fmt.Errorf("publication: %w", err)
		}
		err = syncFactoryBudgetDirectory(dir)
		_ = dir.Close()
		if err != nil {
			return fmt.Errorf("publication: %w", err)
		}
	}
	if err := root.Rename(".runtime.stage", "runtime"); err != nil {
		return fmt.Errorf("publish runtime: %w", err)
	}
	return syncFactoryBudgetDirectory(root)
}

func verifyFactoryExistingBundle(root *os.Root, config factoryRuntimeConfig, data []byte) error {
	bundle, err := openFactoryRuntimeDirectory(root, "runtime")
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	defer bundle.Close()
	existing, err := readFactoryRuntimeFile(bundle, "runtime.json", factoryRuntimeConfigMaxBytes)
	if err != nil || !bytes.Equal(existing, data) {
		return errors.New("conflicting runtime bundle")
	}
	if err := verifyFactoryRuntimeSnapshot(bundle, config); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	return removeFactoryRuntimeStage(root, config)
}

// removeFactoryRuntimeStage accepts only the bounded partial tree this writer can create.
func removeFactoryRuntimeStage(root *os.Root, config factoryRuntimeConfig) error {
	stage, err := openFactoryRuntimeDirectory(root, ".runtime.stage")
	if err != nil {
		if _, e := root.Lstat(".runtime.stage"); errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("publication: %w", err)
	}
	defer stage.Close()
	hash := factoryHostDigest([]byte(config.DockerContext))
	allowed := map[string]any{"runtime.json": int64(factoryRuntimeConfigMaxBytes), "docker": map[string]any{"config.json": int64(2), "contexts": map[string]any{"meta": map[string]any{hash: map[string]any{"meta.json": int64(factoryRuntimeContextMaxBytes)}}}}}
	if err := clearFactoryRuntimeStage(stage, allowed, false); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := clearFactoryRuntimeStage(stage, allowed, true); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := root.Remove(".runtime.stage"); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	return syncFactoryBudgetDirectory(root)
}

func clearFactoryRuntimeStage(root *os.Root, allowed map[string]any, remove bool) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	names, err := dir.Readdirnames(len(allowed) + 1)
	_ = dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("publication: %w", err)
	}
	if len(names) > len(allowed) {
		return errors.New("extra runtime stage entries")
	}
	for _, name := range names {
		entry, ok := allowed[name]
		if !ok {
			return errors.New("unexpected runtime stage entry")
		}
		if err := processFactoryRuntimeStageEntry(root, name, entry, remove); err != nil {
			return err
		}
	}
	return syncFactoryBudgetDirectory(root)
}

func processFactoryRuntimeStageEntry(root *os.Root, name string, entry any, remove bool) error {
	switch value := entry.(type) {
	case int64:
		if remove {
			return removeFactoryPublicationStage(root, name, value)
		}
		return validateFactoryRuntimeStageFile(root, name, value)
	case map[string]any:
		child, err := openFactoryRuntimeDirectory(root, name)
		if err != nil {
			return err
		}
		err = clearFactoryRuntimeStage(child, value, remove)
		_ = child.Close()
		if err != nil {
			return err
		}
		if !remove {
			return nil
		}
		if err := root.Remove(name); err != nil {
			return fmt.Errorf("remove stage directory: %w", err)
		}
		return nil
	default:
		return errors.New("invalid runtime stage specification")
	}
}

func validateFactoryRuntimeStageFile(root *os.Root, name string, value int64) error {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || !factoryHostPrivateOwner(info) || info.Size() > value || (info.Mode().Perm() != 0o400 && info.Mode().Perm() != 0o600) {
		return errors.New("invalid runtime stage file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("invalid runtime stage inode")
	}
	return nil
}
