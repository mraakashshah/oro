package factory

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
)

// A stopped keeper does not retain tmpfs contents. The next source operation
// must start this exact guard and establish continuous readiness before seeding.
func (d *managedDockerVolumes) createSourceKeeper(ctx context.Context) (managedDockerKeeperIntent, error) {
	ctx, cancel := d.operation(ctx)
	defer cancel()
	if !managedKeeperImage(d.manifest.Spec.Runtime.ImageDigest) {
		return managedDockerKeeperIntent{}, errors.New("keeper requires the exact derived guard image ID")
	}
	volume, err := d.createVolume(ctx)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	return d.createKeeperForVolume(ctx, volume.Volume)
}

func (d *managedDockerVolumes) createKeeperForVolume(ctx context.Context, volume ScopeHandle) (managedDockerKeeperIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx, cancel := d.operation(ctx)
	defer cancel()
	if err := d.ready(ctx); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	deadline, _ := ctx.Deadline()
	intent, err := reserveManagedDockerKeeper(ctx, d.manifest, d.journal, d.generation, volume, deadline, d.config.RuntimeConfigSHA256)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	return d.dispatchKeeperWithin(ctx, intent)
}

func (d *managedDockerVolumes) dispatchPreparedKeeper(ctx context.Context, keeper ScopeHandle) (managedDockerKeeperIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	intent, err := loadManagedDockerKeeper(ctx, d.journal, keeper)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, intent.CreateDeadline)
	defer cancel()
	return d.dispatchKeeperWithin(ctx, intent)
}

func (d *managedDockerVolumes) dispatchKeeperWithin(ctx context.Context, intent managedDockerKeeperIntent) (managedDockerKeeperIntent, error) {
	if err := d.ready(ctx); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	if intent.Keeper.Generation != d.generation || intent.RuntimeConfigSHA256 != d.config.RuntimeConfigSHA256 {
		return managedDockerKeeperIntent{}, errors.New("keeper dispatch differs from current owner or frozen configuration")
	}
	call := d.keeperCreateInvocation(intent)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	if _, err := dispatchManagedDockerKeeper(ctx, d.journal, intent.Keeper, d.config.RuntimeConfigSHA256); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	result, err := d.invoke(ctx, call)
	id := strings.TrimSuffix(string(result.Stdout), "\n")
	if err == nil && (result.ExitCode != 0 || len(result.Stderr) != 0 || len(result.Stdout) != 65 || !managedKeeperID(id)) {
		err = errors.New("keeper create lacks exact full-ID acknowledgment")
	}
	if err == nil {
		err = acknowledgeManagedDockerKeeper(ctx, d.journal, intent.Keeper, intent.CreateHost, id)
	}
	if err != nil {
		d.failed = err
		return managedDockerKeeperIntent{}, err
	}
	bound, err := d.inspectKeeperWithin(ctx, intent.Keeper)
	if err != nil {
		d.failed = err
		return managedDockerKeeperIntent{}, err
	}
	return bound, nil
}

func (d *managedDockerVolumes) keeperCreateInvocation(intent managedDockerKeeperIntent) HostInvocation {
	args := make([]string, 0, 43+2*len(intent.Binding.Labels))
	args = append(args, "--config", d.config.DockerConfig, "--context", d.config.DockerContext, "container", "create", "--name", intent.Keeper.Name, "--pull=never", "--init=false", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--ipc", "private", "--cgroupns", "private", "--restart", "no", "--log-driver", "none", "--memory", "67108864", "--memory-swap", "67108864", "--cpus", "0.25", "--pids-limit", "16", "--shm-size", "1048576", "--user", "65534:65534", "--entrypoint", "/usr/local/bin/oro-factory-guard", "--mount", "type=volume,source="+intent.Volume.Name+",target=/source,volume-nocopy")
	for _, key := range slices.Sorted(maps.Keys(intent.Binding.Labels)) {
		args = append(args, "--label", key+"="+intent.Binding.Labels[key])
	}
	args = append(args, intent.Binding.ImageID, "--deadline", intent.GuardDeadline.Format(time.RFC3339Nano))
	return d.invocation(intent.CreateHost, intent.CreateDeadline, args)
}

func (d *managedDockerVolumes) inspectKeeperWithin(ctx context.Context, keeper ScopeHandle) (managedDockerKeeperIntent, error) {
	if err := d.ready(ctx); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	intent, err := loadManagedDockerKeeper(ctx, d.journal, keeper)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	if intent.Phase != "acknowledged" || keeper.Generation != d.generation {
		return managedDockerKeeperIntent{}, errors.New("keeper inspection requires its acknowledged current owner")
	}
	volume, err := loadManagedDockerVolume(ctx, d.journal, intent.Volume)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return managedDockerKeeperIntent{}, errors.New("keeper inspection requires a bounded operation")
	}
	args := []string{"--config", d.config.DockerConfig, "--context", d.config.DockerContext, "container", "inspect", intent.Binding.ContainerID}
	call := d.invocation(ScopeHandle{}, deadline, args)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	call.Scope, err = reserveManagedHostInvocation(ctx, d.journal, d.generation, deadline)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	binding, err := parseManagedDockerKeeperInspection(result, intent, volume)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	if err := bindManagedDockerKeeper(ctx, d.journal, d.generation, keeper, binding); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	intent.Binding = binding
	return intent, nil
}
