package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

type factoryDockerInspection struct {
	ID          string `json:"Id"`
	Name, Image string
	Config      struct {
		User                         string
		Entrypoint, Cmd              []string
		Labels                       map[string]string
		Tty, OpenStdin, AttachStdout bool
	}
	State      factoryDockerState
	HostConfig factoryDockerHostConfig
}

type factoryDockerState struct {
	Status, Error                     string
	Running, Paused, Restarting, Dead bool
	Pid, ExitCode                     int
	StartedAt, FinishedAt             time.Time
}

type factoryDockerHostConfig struct {
	NetworkMode, PidMode, IpcMode, CgroupnsMode      string
	ReadonlyRootfs, Privileged                       bool
	CapAdd, CapDrop, SecurityOpt                     []string
	Memory, MemorySwap, NanoCpus, PidsLimit, ShmSize int64
	RestartPolicy                                    struct {
		Name              string
		MaximumRetryCount int
	}
	LogConfig struct {
		Type   string
		Config map[string]string
	}
}

func validateFactoryDockerInspection(r factoryDockerReadyRequest, data []byte) error {
	var values []factoryDockerInspection
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("decode bounded factory Docker inspection: %w", err)
	}
	if len(values) != 1 {
		return errors.New("factory Docker inspection requires one exact container")
	}
	v := values[0]
	if err := validateFactoryDockerGuardIdentity(r, v); err != nil {
		return err
	}
	if err := validateFactoryDockerLabels(r, v.Config.Labels); err != nil {
		return err
	}
	if err := validateFactoryDockerCreated(v.State); err != nil {
		return err
	}
	return validateFactoryDockerHardening(r.ScopeKind, v.HostConfig)
}

func validateFactoryDockerGuardIdentity(r factoryDockerReadyRequest, v factoryDockerInspection) error {
	name := "/oro-factory-" + r.ObjectiveHash + "-" + r.ScopeKind + "-" + r.ScopeID + "-" + r.ScopeNonce
	if v.ID != r.ContainerID || v.Name != name || v.Image != r.Image || v.Config.User != "65534:65534" ||
		!reflect.DeepEqual(v.Config.Entrypoint, []string{"/usr/local/bin/oro-factory-guard"}) ||
		!reflect.DeepEqual(v.Config.Cmd, []string{"--deadline", r.GuardDeadline.UTC().Format(time.RFC3339Nano)}) ||
		v.Config.Tty || v.Config.OpenStdin || !v.Config.AttachStdout {
		return errors.New("factory Docker container differs from its exact guard identity")
	}
	return nil
}

func validateFactoryDockerCreated(state factoryDockerState) error {
	if state.Status != "created" || state.Running || state.Paused || state.Restarting || state.Dead ||
		state.Pid != 0 || state.ExitCode != 0 || state.Error != "" || !state.StartedAt.IsZero() || !state.FinishedAt.IsZero() {
		return errors.New("factory Docker readiness requires a never-started stopped container")
	}
	return nil
}

func validateFactoryDockerLabels(r factoryDockerReadyRequest, labels map[string]string) error {
	want := map[string]string{"objective": r.ObjectiveHash, "scope": r.ScopeID, "nonce": r.ScopeNonce, "generation": strconv.FormatUint(r.Generation, 10), "kind": r.ScopeKind, "policy": r.PolicyHash}
	for key, value := range want {
		if labels["dev.getoro.factory."+key] != value {
			return errors.New("factory Docker ownership labels differ from exact scope")
		}
	}
	return nil
}

func validateFactoryDockerHardening(kind string, h factoryDockerHostConfig) error {
	if h.NetworkMode != "none" || !h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || !reflect.DeepEqual(h.CapDrop, []string{"ALL"}) ||
		!reflect.DeepEqual(h.SecurityOpt, []string{"no-new-privileges"}) || h.PidMode != "" || h.IpcMode != "private" || h.CgroupnsMode != "private" ||
		h.RestartPolicy.Name != "no" || h.RestartPolicy.MaximumRetryCount != 0 || h.LogConfig.Type != "none" || len(h.LogConfig.Config) != 0 {
		return errors.New("factory Docker guard requires hardened private namespaces")
	}
	return validateFactoryDockerLimits(kind, h)
}

func validateFactoryDockerLimits(kind string, h factoryDockerHostConfig) error {
	if h.Memory <= 0 || h.MemorySwap != h.Memory || h.NanoCpus <= 0 || h.PidsLimit <= 0 || h.ShmSize <= 0 {
		return errors.New("factory Docker guard requires finite positive resource limits")
	}
	if kind == "keeper" && (h.Memory != 64<<20 || h.NanoCpus != 250000000 || h.PidsLimit != 16 || h.ShmSize != 1<<20) {
		return errors.New("factory Docker keeper limits differ from its fixed allowance")
	}
	return nil
}
