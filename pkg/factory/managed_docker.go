package factory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"
)

type managedDockerVolumes struct {
	mu         sync.Mutex
	failed     error
	manifest   managedManifest
	generation uint64
	journal    *managedJournal
	host       HostRuntime
	readConfig ManagedDockerConfigReader
	config     ManagedDockerConfig
	limits     HostRetentionLimits
}

func newManagedDockerVolumes(ctx context.Context, manifest managedManifest, generation uint64, journal *managedJournal, host HostRuntime, readConfig ManagedDockerConfigReader) (*managedDockerVolumes, error) {
	if journal == nil || host == nil || readConfig == nil || generation == 0 || generation > math.MaxInt64 {
		return nil, errors.New("managed Docker requires a journal, trusted runtime, config reader and ownership generation")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("managed Docker initialization canceled: %w", err)
	}
	if err := validateManagedManifest(manifest.Reservation.StateRoot, manifest.Spec.ID, manifest); err != nil {
		return nil, err
	}
	if err := verifyManagedAdmissionIdentity(ctx, journal, manifest); err != nil {
		return nil, err
	}
	limits, err := ManagedHostRetention(manifest.Spec)
	if err != nil {
		return nil, err
	}
	if manifest.Spec.Runtime.HostRequiredBytes < limits.MinimumBytes {
		return nil, errors.New("managed Docker host reservation is below complete retention bound")
	}
	config, err := readConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read managed Docker configuration: %w", err)
	}
	if err := validateManagedDockerConfig(config, manifest); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("managed Docker initialization expired: %w", err)
	}
	return &managedDockerVolumes{manifest: manifest, generation: generation, journal: journal, host: host, readConfig: readConfig, config: config, limits: limits}, nil
}

func (d *managedDockerVolumes) operation(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := minManagedDockerDeadline(time.Now().Add(time.Duration(d.manifest.Spec.StepTimeoutSeconds)*time.Second), d.manifest.Deadline)
	return context.WithDeadline(ctx, deadline)
}

func minManagedDockerDeadline(first, second time.Time) time.Time {
	if second.Before(first) {
		return second
	}
	return first
}

func (d *managedDockerVolumes) ready(ctx context.Context) error {
	if d.failed != nil {
		return fmt.Errorf("managed Docker requires reconciliation after uncertain runtime completion: %w", d.failed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("managed Docker operation canceled: %w", err)
	}
	config, err := d.readConfig(ctx)
	if err != nil {
		return fmt.Errorf("verify managed Docker configuration: %w", err)
	}
	sameHelper := EqualModelAdapterConfig(config.ModelHelper, d.config.ModelHelper)
	original := d.config
	original.ModelHelper = nil
	config.ModelHelper = nil
	if !sameHelper || config != original {
		return errors.New("managed Docker frozen runtime configuration changed")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("managed Docker configuration arrived after deadline: %w", err)
	}
	return nil
}

func (d *managedDockerVolumes) createVolume(ctx context.Context) (managedDockerVolumeIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx, cancel := d.operation(ctx)
	defer cancel()
	if err := d.ready(ctx); err != nil {
		return managedDockerVolumeIntent{}, err
	}
	deadline, _ := ctx.Deadline()
	intent, err := reserveManagedDockerVolume(ctx, d.manifest, d.journal, d.generation, deadline, d.config.RuntimeConfigSHA256)
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	return d.dispatchWithin(ctx, intent)
}

func (d *managedDockerVolumes) dispatchPrepared(ctx context.Context, volume ScopeHandle) (managedDockerVolumeIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	intent, err := loadManagedDockerVolume(ctx, d.journal, volume)
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, intent.CreateDeadline)
	defer cancel()
	return d.dispatchWithin(ctx, intent)
}

func (d *managedDockerVolumes) dispatchWithin(ctx context.Context, intent managedDockerVolumeIntent) (managedDockerVolumeIntent, error) {
	if intent.Volume.Generation != d.generation || intent.RuntimeConfigSHA256 != d.config.RuntimeConfigSHA256 {
		return managedDockerVolumeIntent{}, errors.New("managed Docker prepared intent belongs to another generation or runtime")
	}
	if err := d.ready(ctx); err != nil {
		return managedDockerVolumeIntent{}, err
	}
	call := d.createInvocation(intent)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return managedDockerVolumeIntent{}, err
	}
	if _, err := dispatchManagedDockerVolume(ctx, d.journal, intent.Volume, d.config.RuntimeConfigSHA256); err != nil {
		return managedDockerVolumeIntent{}, err
	}
	result, err := d.invoke(ctx, call)
	if err == nil && (result.ExitCode != 0 || string(result.Stdout) != intent.Binding.Name+"\n" || len(result.Stderr) != 0) {
		err = errors.New("managed Docker create lacks its exact successful acknowledgment")
	}
	if err == nil {
		err = acknowledgeManagedDockerVolume(ctx, d.journal, intent.Volume, intent.CreateHost)
	}
	if err != nil {
		d.failed = err
		return managedDockerVolumeIntent{}, err
	}
	bound, present, err := d.inspectWithin(ctx, intent.Volume)
	if err == nil && !present {
		err = errors.New("acknowledged Docker volume was not positively observed")
	}
	if err != nil {
		d.failed = err
		return managedDockerVolumeIntent{}, err
	}
	return bound, nil
}

func (d *managedDockerVolumes) createInvocation(intent managedDockerVolumeIntent) HostInvocation {
	args := make([]string, 0, 15+2*len(intent.Binding.Labels))
	args = append(args, "--config", d.config.DockerConfig, "--context", d.config.DockerContext, "volume", "create", "--driver", "local")
	for _, key := range slices.Sorted(maps.Keys(intent.Binding.Labels)) {
		args = append(args, "--label", key+"="+intent.Binding.Labels[key])
	}
	args = append(args, "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", "o="+intent.Binding.VolumeOptions["o"], intent.Binding.Name)
	return d.invocation(intent.CreateHost, intent.CreateDeadline, args)
}

func (d *managedDockerVolumes) invocation(scope ScopeHandle, deadline time.Time, args []string) HostInvocation {
	return HostInvocation{Scope: scope, Path: d.config.DockerPath, Args: args, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}, Dir: d.config.StateRoot, Deadline: deadline, OutputLimit: 1 << 20}
}

func (d *managedDockerVolumes) invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	result, err := d.host.Invoke(ctx, call)
	if err != nil {
		return HostResult{}, fmt.Errorf("managed Docker runtime: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return HostResult{}, fmt.Errorf("managed Docker result arrived after operation deadline: %w", err)
	}
	if !time.Now().Before(call.Deadline) || !boundedManagedDockerOutput(result) {
		return HostResult{}, errors.New("managed Docker result exceeds deadline or output bounds")
	}
	observation, err := d.journal.Observe(ctx, call.Scope)
	if err != nil || observation.Phase != "quiescent" || !observation.Deadline.Equal(call.Deadline) {
		return HostResult{}, errors.Join(errors.New("managed Docker runtime lacks exact positive host retirement"), err)
	}
	return result, nil
}

func (d *managedDockerVolumes) inspectVolume(ctx context.Context, volume ScopeHandle) (managedDockerVolumeIntent, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx, cancel := d.operation(ctx)
	defer cancel()
	return d.inspectWithin(ctx, volume)
}

// Inspection and absence are observations only, never daemon-drain or resource
// retirement evidence. An exact discovered lifetime does not acknowledge create.
func (d *managedDockerVolumes) inspectWithin(ctx context.Context, volume ScopeHandle) (managedDockerVolumeIntent, bool, error) {
	if err := d.ready(ctx); err != nil {
		return managedDockerVolumeIntent{}, false, err
	}
	intent, err := loadManagedDockerVolume(ctx, d.journal, volume)
	if err != nil {
		return managedDockerVolumeIntent{}, false, err
	}
	if intent.Phase == "prepared" || volume.Generation > d.generation {
		return managedDockerVolumeIntent{}, false, errors.New("managed Docker inspection requires an owned dispatched intent")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return managedDockerVolumeIntent{}, false, errors.New("managed Docker inspection requires a bounded operation")
	}
	args := []string{"--config", d.config.DockerConfig, "--context", d.config.DockerContext, "volume", "inspect", intent.Binding.Name}
	call := d.invocation(ScopeHandle{}, deadline, args)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return managedDockerVolumeIntent{}, false, err
	}
	call.Scope, err = reserveManagedHostInvocation(ctx, d.journal, d.generation, deadline)
	if err != nil {
		return managedDockerVolumeIntent{}, false, err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		d.failed = err
		return managedDockerVolumeIntent{}, false, err
	}
	binding, present, err := parseManagedDockerVolumeInspection(result, intent.Binding)
	if err != nil {
		d.failed = err
		return managedDockerVolumeIntent{}, false, err
	}
	if !present {
		return intent, false, nil
	}
	if err := bindManagedDockerVolume(ctx, d.journal, d.generation, volume, binding); err != nil {
		d.failed = err
		return managedDockerVolumeIntent{}, false, err
	}
	intent.Binding = binding
	return intent, true, nil
}
