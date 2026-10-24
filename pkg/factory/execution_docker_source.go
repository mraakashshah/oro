package factory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// A handle selects durable source state; it is not a readiness certificate.
type managedDockerSource struct{ Keeper ScopeHandle }

func (d *managedDockerVolumes) encodeSource(ctx context.Context, entries []sourceEntry) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("source encoding canceled: %w", err)
	}
	seed, err := encodeDockerSourceArchive(entries, sourceLimits{MaxArchiveBytes: d.limits.ArchiveBytes, MaxBytes: d.manifest.Spec.Runtime.SourceBytes, MaxFileBytes: d.manifest.Spec.Runtime.SourceBytes, MaxFiles: d.manifest.Spec.Runtime.MaxFiles, MaxPathBytes: d.limits.MaxPathBytes})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("source encoding expired: %w", err)
	}
	return seed, nil
}

func (d *managedDockerVolumes) prepareSource(ctx context.Context, entries []sourceEntry) (managedDockerSource, error) {
	ctx, cancel := d.operation(ctx)
	defer cancel()
	seed, err := d.encodeSource(ctx, entries)
	if err != nil {
		return managedDockerSource{}, err
	}
	if err := requireManagedSourceSchema(ctx, d.journal); err != nil {
		return managedDockerSource{}, err
	}
	// createSourceKeeper owns its lock and performs the real volume and stopped
	// keeper create/inspect chain under this unchanged parent operation deadline.
	keeper, err := d.createSourceKeeper(ctx)
	if err != nil {
		return managedDockerSource{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ready(ctx); err != nil {
		return managedDockerSource{}, err
	}
	deadline, _ := ctx.Deadline()
	source, err := reserveManagedDockerSource(ctx, d.manifest, d.journal, d.generation, keeper.Keeper, deadline, contentHash(seed), int64(len(seed)))
	if err != nil {
		return managedDockerSource{}, err
	}
	return d.continueSource(ctx, source, seed)
}

func (d *managedDockerVolumes) resumeSource(ctx context.Context, keeper ScopeHandle, entries []sourceEntry) (managedDockerSource, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	source, err := loadManagedDockerSource(ctx, d.journal, keeper)
	if err != nil {
		return managedDockerSource{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, source.OperationDeadline)
	defer cancel()
	seed, err := d.encodeSource(ctx, entries)
	if err != nil {
		return managedDockerSource{}, err
	}
	if source.SeedSHA256 != contentHash(seed) || source.SeedBytes != int64(len(seed)) {
		return managedDockerSource{}, errors.New("source resume archive differs from frozen seed")
	}
	return d.continueSource(ctx, source, seed)
}

func (d *managedDockerVolumes) continueSource(ctx context.Context, source managedDockerSourceRecord, seed []byte) (managedDockerSource, error) {
	for {
		if err := d.sourceReady(ctx, source); err != nil {
			return managedDockerSource{}, err
		}
		done, err := d.advanceSource(ctx, source, seed)
		if err != nil {
			d.failed = err
			return managedDockerSource{}, err
		}
		if done {
			return managedDockerSource{Keeper: source.Keeper}, nil
		}
		source, err = loadManagedDockerSource(ctx, d.journal, source.Keeper)
		if err != nil {
			d.failed = err
			return managedDockerSource{}, err
		}
	}
}

func (d *managedDockerVolumes) sourceReady(ctx context.Context, s managedDockerSourceRecord) error {
	if s.Keeper.Generation != d.generation || s.RuntimeConfigSHA256 != d.config.RuntimeConfigSHA256 {
		return errors.New("source belongs to another owner or runtime")
	}
	if s.Phase == "start_dispatching" || s.Phase == "seed_dispatching" {
		return errors.New("source has an uncertain dispatched effect requiring reconciliation")
	}
	if err := d.ready(ctx); err != nil {
		return err
	}
	return changeManagedDockerSource(ctx, d.journal, s.Keeper, "", func(tx *sql.Tx, state *managedSupervision, current *managedDockerSourceRecord) (bool, error) {
		return false, activeManagedSource(ctx, tx, state, *current, d.generation)
	})
}

func (d *managedDockerVolumes) advanceSource(ctx context.Context, s managedDockerSourceRecord, seed []byte) (bool, error) {
	switch s.Phase {
	case "start_prepared":
		return false, d.startSource(ctx, s)
	case "start_acknowledged":
		started, err := d.inspectSource(ctx, s)
		if err != nil {
			return false, err
		}
		return false, recordManagedDockerSourceReady(ctx, d.journal, d.generation, s.Keeper, started)
	case "ready":
		_, err := reserveManagedDockerSeed(ctx, d.journal, d.generation, s.Keeper)
		return false, err
	case "seed_prepared":
		return false, d.seedSource(ctx, s, seed)
	case "seed_acknowledged", "seeded":
		started, err := d.inspectSource(ctx, s)
		if err != nil {
			return false, err
		}
		return true, completeManagedDockerSeed(ctx, d.journal, d.generation, s.Keeper, started)
	default:
		return false, errors.New("source phase cannot execute")
	}
}

func (d *managedDockerVolumes) startSource(ctx context.Context, s managedDockerSourceRecord) error {
	keeper, err := loadManagedDockerKeeper(ctx, d.journal, s.Keeper)
	if err != nil {
		return err
	}
	call := d.sourceReadyInvocation(s, keeper)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return err
	}
	if _, err := dispatchManagedDockerSourceStart(ctx, d.journal, s.Keeper, d.config.RuntimeConfigSHA256); err != nil {
		return err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 || string(result.Stdout) != "READY "+keeper.Binding.ContainerID+" "+keeper.GuardDeadline.Format(time.RFC3339Nano)+"\n" {
		return errors.New("source start lacks exact READY acknowledgment")
	}
	return acknowledgeManagedDockerSourceStart(ctx, d.journal, s.Keeper, s.StartHost)
}

func (d *managedDockerVolumes) sourceReadyInvocation(s managedDockerSourceRecord, k managedDockerKeeperIntent) HostInvocation {
	call := d.invocation(s.StartHost, s.OperationDeadline, []string{
		"factory", "_docker-start-ready", "--docker", d.config.DockerPath, "--docker-sha256", d.config.DockerSHA256,
		"--docker-config", d.config.DockerConfig, "--context", d.config.DockerContext, "--container", k.Binding.ContainerID,
		"--scope-id", k.Keeper.ID, "--scope-nonce", k.Keeper.Nonce, "--scope-generation", strconv.FormatUint(k.Keeper.Generation, 10),
		"--scope-kind", "keeper", "--objective-hash", cleanupObjectiveHash(d.journal.admission), "--policy-hash", d.manifest.PolicyHash,
		"--image", k.Binding.ImageID, "--guard-deadline", k.GuardDeadline.Format(time.RFC3339Nano), "--deadline", s.OperationDeadline.Format(time.RFC3339Nano),
	})
	call.Path = d.config.OroPath
	call.OutputLimit = 256
	return call
}

func (d *managedDockerVolumes) seedSource(ctx context.Context, s managedDockerSourceRecord, seed []byte) error {
	keeper, err := loadManagedDockerKeeper(ctx, d.journal, s.Keeper)
	if err != nil {
		return err
	}
	call := d.invocation(s.SeedHost, s.OperationDeadline, []string{"--config", d.config.DockerConfig, "--context", d.config.DockerContext, "cp", "-", keeper.Binding.ContainerID + ":/source"})
	call.Stdin = seed
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return err
	}
	if _, err := dispatchManagedDockerSeed(ctx, d.journal, s.Keeper, contentHash(seed), int64(len(seed))); err != nil {
		return err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		return errors.New("source copy lacks exact successful acknowledgment")
	}
	return acknowledgeManagedDockerSeed(ctx, d.journal, s.Keeper, s.SeedHost)
}

func (d *managedDockerVolumes) inspectSource(ctx context.Context, s managedDockerSourceRecord) (string, error) {
	keeper, err := loadManagedDockerKeeper(ctx, d.journal, s.Keeper)
	if err != nil {
		return "", err
	}
	volume, err := loadManagedDockerVolume(ctx, d.journal, s.Volume)
	if err != nil {
		return "", err
	}
	call := d.invocation(ScopeHandle{}, s.OperationDeadline, []string{"--config", d.config.DockerConfig, "--context", d.config.DockerContext, "container", "inspect", keeper.Binding.ContainerID})
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return "", err
	}
	call.Scope, err = reserveManagedHostInvocation(ctx, d.journal, d.generation, s.OperationDeadline)
	if err != nil {
		return "", err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return "", err
	}
	return parseManagedDockerRunningKeeperInspection(result, keeper, volume, s.StartedAt)
}
