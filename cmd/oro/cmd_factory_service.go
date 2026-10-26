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
	if err := access.transition(ctx, intent, "prepared", "dispatching"); err != nil {
		return fmt.Errorf("service dispatch uncertain: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service dispatch uncertain: %w", err)
	}
	plist := filepath.Join(access.root, access.id, "service", "service.plist")
	if err := bootstrap(ctx, plist); err != nil {
		return fmt.Errorf("service bootstrap uncertain: %w", err)
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
	lockPath := filepath.Join(access.root, ".managed", "locks", access.id, "service.lock")
	entry, err := os.Lstat(lockPath)
	stat, ok := info.Sys().(*syscall.Stat_t)
	if err != nil || !os.SameFile(info, entry) || !entry.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 || !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return errors.New("service lock identity changed")
	}
	return nil
}
