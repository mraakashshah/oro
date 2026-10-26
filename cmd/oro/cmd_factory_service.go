package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"oro/pkg/factory"
)

func registerFactoryServiceLocked(ctx context.Context, access *factoryServiceAccess, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference) error {
	if err := factoryServicePlatformSupported(); err != nil {
		return err
	}
	return registerFactoryServiceLockedWithBootstrap(ctx, access, view, ref, bootstrapFactoryService)
}

func registerFactoryServiceLockedWithBootstrap(ctx context.Context, access *factoryServiceAccess, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, bootstrap func(context.Context, string) error) error {
	if err := validateFactoryServiceAccess(access, bootstrap); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, view.Deadline)
	defer cancel()
	intent, registration, err := access.prepare(ctx, view, ref)
	if err != nil {
		return fmt.Errorf("prepare service registration: %w", err)
	}
	switch registration.Phase {
	case "registered":
		return nil
	case "dispatching":
		return errors.New("service registration uncertain: bootstrap already dispatched")
	case "prepared":
	default:
		return errors.New("invalid service registration phase")
	}
	if err := validateFactoryServiceAccess(access, bootstrap); err != nil {
		return err
	}
	if err := access.transition(ctx, intent, "prepared", "dispatching"); err != nil {
		return fmt.Errorf("service dispatch uncertain: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service dispatch uncertain: %w", err)
	}
	if err := validateFactoryServiceAccess(access, bootstrap); err != nil {
		return fmt.Errorf("service dispatch uncertain: %w", err)
	}
	plist := filepath.Join(access.root, access.id, "service", "service.plist")
	if err := bootstrap(ctx, plist); err != nil {
		return fmt.Errorf("service bootstrap uncertain: %w", err)
	}
	if err := validateFactoryServiceAccess(access, bootstrap); err != nil {
		return fmt.Errorf("service acknowledgement uncertain: %w", err)
	}
	if err := access.transition(ctx, intent, "dispatching", "registered"); err != nil {
		return fmt.Errorf("service acknowledgement uncertain: %w", err)
	}
	return nil
}

func bootstrapFactoryServiceWithRunner(ctx context.Context, plist string, run func(context.Context, ...string) (int, error)) error {
	code, err := run(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plist)
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("launchctl bootstrap exited %d", code)
	}
	return nil
}

func validateFactoryServiceAccess(access *factoryServiceAccess, bootstrap func(context.Context, string) error) error {
	if access == nil || access.lock == nil || bootstrap == nil {
		return errors.New("service registration requires locked access and bootstrap")
	}
	info, err := access.lock.Stat()
	if err != nil {
		return fmt.Errorf("service lock unavailable: %w", err)
	}
	dir, err := openFactoryServiceLockDirectory(access)
	if err != nil {
		return err
	}
	defer dir.Close()
	entry, err := dir.Lstat("service.lock")
	stat, ok := info.Sys().(*syscall.Stat_t)
	if err != nil || !os.SameFile(info, entry) || !entry.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 || !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return errors.New("service lock identity changed")
	}
	return nil
}

func openFactoryServiceLockDirectory(access *factoryServiceAccess) (*os.Root, error) {
	if err := validateFactoryServiceRoot(access.root, access.id); err != nil {
		return nil, fmt.Errorf("service lock root: %w", err)
	}
	dir, err := os.OpenRoot(access.root)
	if err != nil {
		return nil, fmt.Errorf("open service lock root: %w", err)
	}
	if err := bindFactoryRuntimeRoot(dir, access.root); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("service lock root: %w", err)
	}
	for _, name := range []string{".managed", "locks", access.id} {
		next, err := openFactoryRuntimeDirectory(dir, name)
		if err != nil {
			_ = dir.Close()
			return nil, fmt.Errorf("service lock directory: %w", err)
		}
		_ = dir.Close()
		dir = next
	}
	return dir, nil
}
