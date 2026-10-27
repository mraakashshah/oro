package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (d *managedDockerVolumes) prepareCandidate(parent context.Context, owner *managedControllerOwner, intent managedCandidateIntent) (managedCandidateIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx, cancel := context.WithDeadline(parent, intent.ExecutionDeadline)
	defer cancel()
	if err := d.candidateReady(ctx, owner, intent); err != nil {
		return managedCandidateIntent{}, err
	}
	if err := d.inspectCandidateKeeper(ctx, owner, intent); err != nil {
		return managedCandidateIntent{}, err
	}
	call := d.candidateCreateInvocation(intent)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return managedCandidateIntent{}, err
	}
	var err error
	intent, err = transitionManagedCandidate(ctx, owner, intent, "prepared", "create_dispatching", "")
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if err := d.candidateReady(ctx, owner, intent); err != nil {
		return managedCandidateIntent{}, err
	}
	// Host Granted is the authorization boundary. A stop after that point may
	// leave an in-flight call; the next owner-checked transition rejects its result.
	result, err := d.invoke(ctx, call)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	id := strings.TrimSuffix(string(result.Stdout), "\n")
	if result.ExitCode != 0 || len(result.Stderr) != 0 || len(result.Stdout) != 65 || !managedKeeperID(id) {
		return managedCandidateIntent{}, errors.New("candidate create lacks exact acknowledgment")
	}
	intent, err = transitionManagedCandidate(ctx, owner, intent, "create_dispatching", "created", id)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	created, err := d.inspectCandidate(ctx, owner, intent, false)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	intent, err = transitionManagedCandidate(ctx, owner, intent, "created", "inspected", created)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	intent, err = transitionManagedCandidate(ctx, owner, intent, "inspected", "start_prepared", "")
	if err != nil {
		return managedCandidateIntent{}, err
	}
	return d.startCandidate(ctx, owner, intent)
}

func (d *managedDockerVolumes) startCandidate(ctx context.Context, owner *managedControllerOwner, intent managedCandidateIntent) (managedCandidateIntent, error) {
	call := d.candidateReadyInvocation(intent)
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return managedCandidateIntent{}, err
	}
	intent, err := transitionManagedCandidate(ctx, owner, intent, "start_prepared", "start_dispatching", "")
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if err := d.candidateReady(ctx, owner, intent); err != nil {
		return managedCandidateIntent{}, err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 || string(result.Stdout) != "READY "+intent.Binding.ContainerID+" "+intent.GuardDeadline.Format(time.RFC3339Nano)+"\n" {
		return managedCandidateIntent{}, errors.New("candidate start lacks exact READY acknowledgment")
	}
	intent, err = transitionManagedCandidate(ctx, owner, intent, "start_dispatching", "start_acknowledged", "")
	if err != nil {
		return managedCandidateIntent{}, err
	}
	started, err := d.inspectCandidate(ctx, owner, intent, true)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if err := d.inspectCandidateKeeper(ctx, owner, intent); err != nil {
		return managedCandidateIntent{}, err
	}
	return transitionManagedCandidate(ctx, owner, intent, "start_acknowledged", "ready", started)
}

func (d *managedDockerVolumes) candidateReady(ctx context.Context, owner *managedControllerOwner, want managedCandidateIntent) error {
	if err := d.ready(ctx); err != nil {
		return err
	}
	if owner == nil || owner.journal != d.journal || owner.scope.Generation != d.generation || owner.runtime != want.Runtime || d.config.RuntimeConfigSHA256 != want.Runtime.SHA256 {
		return errors.New("candidate owner or runtime differs")
	}
	return owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		i, err := readManagedCandidate(ctx, tx, owner, want.WorkspaceID)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(i, want) {
			return false, errors.New("candidate identity changed")
		}
		if err := requireNoManagedCleanup(ctx, tx); err != nil {
			return false, err
		}
		return false, requireCandidateSourceScopes(ctx, tx, i.Source)
	})
}

func (d *managedDockerVolumes) inspectCandidateKeeper(ctx context.Context, owner *managedControllerOwner, i managedCandidateIntent) error {
	if err := d.candidateReady(ctx, owner, i); err != nil {
		return err
	}
	keeper, err := loadManagedDockerKeeper(ctx, d.journal, i.Source.Keeper)
	if err != nil {
		return err
	}
	if candidateKeeperDigest(d.journal, keeper) != i.KeeperSHA256 {
		return errors.New("candidate keeper identity changed")
	}
	volume, err := loadManagedDockerVolume(ctx, d.journal, i.Source.Volume)
	if err != nil {
		return err
	}
	call := d.invocation(ScopeHandle{}, i.ExecutionDeadline, []string{"--config", d.config.DockerConfig, "--context", d.config.DockerContext, "container", "inspect", keeper.Binding.ContainerID})
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return err
	}
	call.Scope, err = reserveManagedHostInvocation(ctx, d.journal, d.generation, i.ExecutionDeadline)
	if err != nil {
		return err
	}
	if err := d.candidateReady(ctx, owner, i); err != nil {
		return err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return err
	}
	_, err = parseManagedDockerRunningKeeperInspection(result, keeper, volume, i.Source.StartedAt)
	return err
}

func (d *managedDockerVolumes) candidateCreateInvocation(i managedCandidateIntent) HostInvocation {
	r := d.manifest.Spec.Runtime
	args := make([]string, 0, 48+2*len(i.Binding.Labels))
	args = append(args, "--config", d.config.DockerConfig, "--context", d.config.DockerContext, "container", "create", "--name", i.Container.Name, "--pull=never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--ipc", "private", "--cgroupns", "private", "--restart", "no", "--log-driver", "none", "--memory", strconv.FormatInt(r.MemoryBytes, 10), "--memory-swap", strconv.FormatInt(r.MemoryBytes, 10), "--cpus", strconv.FormatFloat(r.CPUs, 'f', 9, 64), "--pids-limit", strconv.Itoa(r.PIDs), "--shm-size", "1048576", "--user", "65534:65534", "--entrypoint", "/usr/local/bin/oro-factory-guard", "--mount", "type=volume,source="+i.Source.Volume.Name+",target=/source,volume-nocopy", "--tmpfs", candidateScratchOptions(r))
	for _, key := range slices.Sorted(maps.Keys(i.Binding.Labels)) {
		args = append(args, "--label", key+"="+i.Binding.Labels[key])
	}
	args = append(args, i.Binding.ImageID, "--deadline", i.GuardDeadline.Format(time.RFC3339Nano))
	return d.invocation(i.CreateHost, i.ExecutionDeadline, args)
}

func candidateScratchOptions(r RuntimePolicy) string {
	return "/scratch:rw,size=" + strconv.FormatInt(r.ScratchBytes, 10) + ",nr_inodes=" + strconv.Itoa(r.ScratchInodes) + ",uid=1000,gid=1000,mode=0700"
}

func (d *managedDockerVolumes) candidateReadyInvocation(i managedCandidateIntent) HostInvocation {
	call := d.invocation(i.StartHost, i.ExecutionDeadline, []string{"factory", "_docker-start-ready", "--docker", d.config.DockerPath, "--docker-sha256", d.config.DockerSHA256, "--docker-config", d.config.DockerConfig, "--context", d.config.DockerContext, "--container", i.Binding.ContainerID, "--scope-id", i.Container.ID, "--scope-nonce", i.Container.Nonce, "--scope-generation", strconv.FormatUint(i.Container.Generation, 10), "--scope-kind", "container", "--objective-hash", cleanupObjectiveHash(d.journal.admission), "--policy-hash", d.manifest.PolicyHash, "--image", i.Binding.ImageID, "--guard-deadline", i.GuardDeadline.Format(time.RFC3339Nano), "--deadline", i.ExecutionDeadline.Format(time.RFC3339Nano)})
	call.Path = d.config.OroPath
	call.OutputLimit = 256
	return call
}

func (d *managedDockerVolumes) inspectCandidate(ctx context.Context, owner *managedControllerOwner, i managedCandidateIntent, running bool) (string, error) {
	if err := d.candidateReady(ctx, owner, i); err != nil {
		return "", err
	}
	keeper, err := loadManagedDockerKeeper(ctx, d.journal, i.Source.Keeper)
	if err != nil {
		return "", err
	}
	if candidateKeeperDigest(d.journal, keeper) != i.KeeperSHA256 {
		return "", errors.New("candidate keeper changed")
	}
	call := d.invocation(ScopeHandle{}, i.ExecutionDeadline, []string{"--config", d.config.DockerConfig, "--context", d.config.DockerContext, "container", "inspect", i.Binding.ContainerID})
	if err := validateManagedHostRequest(call, d.limits); err != nil {
		return "", err
	}
	call.Scope, err = reserveManagedHostInvocation(ctx, d.journal, d.generation, i.ExecutionDeadline)
	if err != nil {
		return "", err
	}
	if err := d.candidateReady(ctx, owner, i); err != nil {
		return "", err
	}
	result, err := d.invoke(ctx, call)
	if err != nil {
		return "", err
	}
	return parseManagedCandidateInspection(result, i, keeper, d.manifest.Spec.Runtime, running)
}

func parseManagedCandidateInspection(result HostResult, i managedCandidateIntent, keeper managedDockerKeeperIntent, r RuntimePolicy, running bool) (string, error) {
	if !boundedManagedDockerOutput(result) || result.ExitCode != 0 || len(result.Stderr) != 0 {
		return "", errors.New("candidate inspect uncertain")
	}
	if err := validateDockerVolumeJSON(result.Stdout); err != nil {
		return "", err
	}
	var objects []json.RawMessage
	if err := json.Unmarshal(result.Stdout, &objects); err != nil || len(objects) != 1 {
		return "", errors.New("candidate inspect requires one object")
	}
	var value managedKeeperInspection
	if err := decodeManagedKeeperObject(objects[0], reflect.ValueOf(&value).Elem()); err != nil {
		return "", err
	}
	if err := validateCandidateInspection(value, i, keeper, r); err != nil {
		return "", err
	}
	created, err := CanonicalDockerCreatedAt(value.Created)
	if err != nil {
		return "", err
	}
	if i.Binding.CreatedAt != "" && created != i.Binding.CreatedAt {
		return "", errors.New("candidate creation lifetime changed")
	}
	if !running {
		if err := validateManagedKeeperStopped(value.State); err != nil {
			return "", err
		}
		return created, nil
	}
	if err := validateManagedKeeperRunning(value.State); err != nil {
		return "", err
	}
	return candidateStartedAt(result.Stdout)
}

func candidateStartedAt(output []byte) (string, error) {
	var raw []struct{ State struct{ StartedAt string } }
	if err := json.Unmarshal(output, &raw); err != nil || len(raw) != 1 {
		return "", errors.New("candidate start timestamp missing")
	}
	started, err := CanonicalDockerCreatedAt(raw[0].State.StartedAt)
	if err != nil {
		return "", fmt.Errorf("candidate start timestamp: %w", err)
	}
	return started, nil
}

func validateCandidateInspection(v managedKeeperInspection, i managedCandidateIntent, keeper managedDockerKeeperIntent, r RuntimePolicy) error {
	if v.ID != i.Binding.ContainerID || v.Name != "/"+i.Container.Name || v.Image != i.Binding.ImageID {
		return errors.New("candidate container identity differs")
	}
	labels := maps.Clone(keeper.Binding.Labels)
	for key, value := range i.Binding.Labels {
		labels[key] = value
	}
	if !reflect.DeepEqual(v.Config.Labels, labels) || v.Config.User != "65534:65534" || !reflect.DeepEqual(v.Config.Entrypoint, []string{"/usr/local/bin/oro-factory-guard"}) || !reflect.DeepEqual(v.Config.Cmd, []string{"--deadline", i.GuardDeadline.Format(time.RFC3339Nano)}) || v.Config.Tty || v.Config.OpenStdin || !v.Config.AttachStdout {
		return errors.New("candidate guard or labels differ")
	}
	return validateCandidateHostConfig(v, i, r)
}

func validateCandidateHostConfig(v managedKeeperInspection, i managedCandidateIntent, r RuntimePolicy) error {
	h := v.HostConfig
	if h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.CgroupnsMode != "private" || !h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || !reflect.DeepEqual(h.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(h.SecurityOpt, []string{"no-new-privileges"}) {
		return errors.New("candidate isolation differs")
	}
	return validateCandidateResources(v, i, r)
}

func validateCandidateResources(v managedKeeperInspection, i managedCandidateIntent, r RuntimePolicy) error {
	h := v.HostConfig
	nano := int64(math.Round(r.CPUs * 1e9))
	if h.Memory != r.MemoryBytes || h.MemorySwap != r.MemoryBytes || h.NanoCpus != nano || h.PidsLimit != int64(r.PIDs) || h.ShmSize != 1<<20 {
		return errors.New("candidate limits differ")
	}
	return validateCandidateExtras(v, i, r)
}

func validateCandidateExtras(v managedKeeperInspection, i managedCandidateIntent, r RuntimePolicy) error {
	h := v.HostConfig
	if h.RestartPolicy.Name != "no" || h.RestartPolicy.MaximumRetryCount != 0 || h.LogConfig.Type != "none" || len(h.LogConfig.Config) != 0 || len(h.Binds) != 0 || len(h.VolumesFrom) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 {
		return errors.New("candidate extras differ")
	}
	if !reflect.DeepEqual(h.Tmpfs, map[string]string{"/scratch": strings.TrimPrefix(candidateScratchOptions(r), "/scratch:")}) {
		return errors.New("candidate scratch differs")
	}
	return validateManagedKeeperMounts(v, i.Source.Volume.Name)
}
