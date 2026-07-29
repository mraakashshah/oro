package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ProviderMaintenance identifies one provider-native maintenance operation.
//
// The cleaner comes from Provider.Cleaner and is executed as its declared
// executable plus argument vector.
//
//oro:testonly — wired into scheduled provider maintenance by subsequent storage work.
type ProviderMaintenance struct {
	Provider CacheProvider
}

// MaintenanceSnapshot records filesystem capacity observations for a provider.
//
//oro:testonly — persisted maintenance evidence is wired by subsequent storage work.
type MaintenanceSnapshot struct {
	FreeBytes uint64 `json:"free_bytes"`
	UsedBytes uint64 `json:"used_bytes"`
}

// MaintenanceEvidence records the outcome of one provider maintenance operation.
//
//oro:testonly — persisted maintenance evidence is wired by subsequent storage work.
type MaintenanceEvidence struct {
	ProviderID string              `json:"provider_id"`
	Before     MaintenanceSnapshot `json:"before"`
	After      MaintenanceSnapshot `json:"after"`
	ExitCode   int                 `json:"exit_code"`
}

// RunProviderMaintenance executes a provider's trusted cleaner without a shell
// and captures filesystem usage before and after the operation.
//
//oro:testonly — scheduled provider maintenance wiring follows in a later task.
func RunProviderMaintenance(ctx context.Context, maintenance ProviderMaintenance) (MaintenanceEvidence, error) {
	if err := ctx.Err(); err != nil {
		return MaintenanceEvidence{}, fmt.Errorf("provider maintenance context: %w", err)
	}
	if err := maintenance.Provider.Validate(); err != nil {
		return MaintenanceEvidence{}, fmt.Errorf("validate provider maintenance: %w", err)
	}
	if !maintenance.Provider.Cleaner.present() {
		return MaintenanceEvidence{}, fmt.Errorf("validate provider maintenance: %w", ErrInvalidProvider)
	}

	root := maintenance.Provider.DefaultPath()
	before, err := maintenanceSnapshot(root)
	if err != nil {
		return MaintenanceEvidence{}, fmt.Errorf("capture provider maintenance before evidence: %w", err)
	}
	evidence := MaintenanceEvidence{ProviderID: maintenance.Provider.ID, Before: before, ExitCode: -1}

	commandErr := runProviderCommand(ctx, maintenance.Provider.Cleaner)
	evidence.ExitCode = commandExitCode(commandErr)
	after, afterErr := maintenanceSnapshot(root)
	if afterErr != nil {
		return evidence, fmt.Errorf("capture provider maintenance after evidence: %w", afterErr)
	}
	evidence.After = after
	if commandErr != nil {
		return evidence, fmt.Errorf("run provider maintenance %q: %w", maintenance.Provider.ID, commandErr)
	}
	return evidence, nil
}

func runProviderCommand(ctx context.Context, cleaner CleanerDescriptor) error {
	err := exec.CommandContext(ctx, cleaner.Executable, cleaner.Args...).Run() //nolint:gosec // CacheProvider validation permits only trusted fixed argv commands.
	if err != nil {
		return fmt.Errorf("run fixed provider command: %w", err)
	}
	return nil
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func maintenanceSnapshot(root string) (MaintenanceSnapshot, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return MaintenanceSnapshot{}, fmt.Errorf("inspect provider filesystem: %w", err)
	}
	used, err := providerUsageBytes(root)
	if err != nil {
		return MaintenanceSnapshot{}, err
	}
	return MaintenanceSnapshot{
		FreeBytes: stat.Bavail * uint64(stat.Bsize),
		UsedBytes: used,
	}, nil
}

func providerUsageBytes(root string) (uint64, error) {
	var used uint64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect provider entry: %w", err)
		}
		used += uint64(info.Size())
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure provider usage: %w", err)
	}
	return used, nil
}
