package main

import (
	"encoding/json"
	"errors"
	"maps"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oro/pkg/factory"
)

// These pure planners accept a permit authenticated by the caller. They cannot
// grant authority or select a program/endpoint. The helper must independently
// bind Context to its frozen runtime config and recheck authority before exec.
func factoryDockerCleanupInspectArgs(permit factory.DockerCleanupPermit) ([]string, error) {
	if err := validateFactoryDockerCleanupPlan(permit); err != nil {
		return nil, err
	}
	if permit.Binding.Kind == "volume" {
		return []string{"volume", "inspect", permit.Binding.Name}, nil
	}
	identity := permit.Binding.ContainerID
	if identity == "" {
		identity = permit.Binding.Name
	}
	return []string{"inspect", "--type", "container", identity}, nil
}

func factoryDockerCleanupMutationArgs(permit factory.DockerCleanupPermit, observed factory.DockerCleanupBinding) ([]string, error) {
	if err := validateFactoryDockerCleanupPlan(permit); err != nil {
		return nil, err
	}
	if permit.Operation == factory.DockerCleanupInspect || !reflect.DeepEqual(permit.Binding, observed) {
		return nil, errors.New("cleanup mutation requires its exact admitted observed lifetime")
	}
	if permit.Binding.Kind == "volume" {
		return []string{"volume", "rm", permit.Binding.Name}, nil
	}
	if permit.Operation == factory.DockerCleanupStop {
		return []string{"stop", "--signal", "SIGKILL", "--timeout", "0", permit.Binding.ContainerID}, nil
	}
	return []string{"rm", "--force", permit.Binding.ContainerID}, nil
}

func validateFactoryDockerCleanupPlan(permit factory.DockerCleanupPermit) error {
	if permit.Operation != factory.DockerCleanupInspect && permit.Operation != factory.DockerCleanupStop && permit.Operation != factory.DockerCleanupRemove {
		return errors.New("cleanup permits only inspection, stop or removal")
	}
	if err := validateFactoryDockerCleanupScope(permit); err != nil {
		return err
	}
	binding := permit.Binding
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`).MatchString(binding.Context) ||
		!factoryDockerCleanupMapBound(binding.Labels) || !factoryDockerCleanupMapBound(binding.VolumeOptions) {
		return errors.New("cleanup context or ownership descriptor is invalid")
	}
	if binding.CreatedAt != "" && !factoryDockerCleanupCreatedAt(binding.CreatedAt) {
		return errors.New("cleanup creation identity must be canonical UTC")
	}
	if err := validateFactoryDockerCleanupObjectKind(permit); err != nil {
		return err
	}
	data, err := json.Marshal(permit)
	if err != nil || len(data) > factory.ManagedCleanupPermitBytes {
		return errors.Join(errors.New("cleanup permit exceeds its bounded descriptor"), err)
	}
	return nil
}

func validateFactoryDockerCleanupScope(permit factory.DockerCleanupPermit) error {
	scope, target, binding := permit.Scope, permit.Target, permit.Binding
	if !factoryDockerCleanupHandle(scope) || !factoryDockerCleanupHandle(target) {
		return errors.New("cleanup scope requires bounded exact identity")
	}
	if err := validateFactoryDockerCleanupNamespace(permit); err != nil {
		return err
	}
	if permit.Deadline.IsZero() || permit.Deadline.Location() != time.UTC || permit.Deadline.Year() < 1 || permit.Deadline.Year() > 9999 {
		return errors.New("cleanup requires its original canonical deadline")
	}
	want := map[string]string{"scope": target.ID, "nonce": target.Nonce, "generation": strconv.FormatUint(target.Generation, 10), "kind": target.Kind}
	for key, value := range want {
		if binding.Labels["dev.getoro.factory."+key] != value {
			return errors.New("cleanup labels differ from exact target ownership")
		}
	}
	return nil
}

func factoryDockerCleanupHandle(handle factory.ScopeHandle) bool {
	return factoryJobLowerHex(handle.ID, 32) && factoryJobLowerHex(handle.Nonce, 32) && handle.Generation > 0 && handle.Generation <= math.MaxInt64
}

func validateFactoryDockerCleanupNamespace(permit factory.DockerCleanupPermit) error {
	scope, target, binding := permit.Scope, permit.Target, permit.Binding
	objective := binding.Labels["dev.getoro.factory.objective"]
	if !factoryJobLowerHex(objective, 16) || !factoryJobLowerHex(binding.Labels["dev.getoro.factory.policy"], 64) ||
		scope.Kind != "cleanup" || scope.Backend != "launchd" || target.Backend != "docker" || target.Generation > scope.Generation ||
		scope.Name != "dev.getoro.factory."+objective+".cleanup."+scope.ID+"."+scope.Nonce ||
		target.Name != "oro-factory-"+objective+"-"+target.Kind+"-"+target.ID+"-"+target.Nonce || binding.Name != target.Name {
		return errors.New("cleanup scope and target do not match exact ownership")
	}
	return nil
}

func validateFactoryDockerCleanupObjectKind(permit factory.DockerCleanupPermit) error {
	if permit.Target.Kind == "volume" {
		return validateFactoryDockerCleanupVolumeBinding(permit)
	}
	return validateFactoryDockerCleanupContainerBinding(permit)
}

func validateFactoryDockerCleanupVolumeBinding(permit factory.DockerCleanupPermit) error {
	binding := permit.Binding
	if binding.Kind != "volume" || binding.ContainerID != "" || binding.ImageID != "" || binding.VolumeDriver != "local" ||
		len(binding.VolumeOptions) != 3 || binding.VolumeOptions["type"] != "tmpfs" || binding.VolumeOptions["device"] != "tmpfs" ||
		binding.VolumeOptions["o"] == "" || permit.Operation == factory.DockerCleanupStop ||
		(permit.Operation == factory.DockerCleanupRemove && binding.CreatedAt == "") {
		return errors.New("cleanup volume must retain its exact local tmpfs binding")
	}
	return nil
}

func validateFactoryDockerCleanupContainerBinding(permit factory.DockerCleanupPermit) error {
	binding := permit.Binding
	if (permit.Target.Kind != "keeper" && permit.Target.Kind != "container") || binding.Kind != "container" || binding.VolumeDriver != "" ||
		len(binding.VolumeOptions) != 0 || !strings.HasPrefix(binding.ImageID, "sha256:") || !factoryJobLowerHex(strings.TrimPrefix(binding.ImageID, "sha256:"), 64) {
		return errors.New("cleanup container must retain its immutable image and resource kind")
	}
	if permit.Operation == factory.DockerCleanupInspect && binding.ContainerID == "" && binding.CreatedAt == "" {
		return nil
	}
	if !factoryJobLowerHex(binding.ContainerID, 64) || binding.CreatedAt == "" {
		return errors.New("cleanup mutation requires full container ID and creation identity")
	}
	return nil
}

func factoryDockerCleanupMapBound(values map[string]string) bool {
	if len(values) > 16 {
		return false
	}
	for key, value := range values {
		if key == "" || len(key) > 128 || len(value) > 1024 || strings.ContainsRune(key, 0) || strings.ContainsRune(value, 0) {
			return false
		}
	}
	return true
}

func factoryDockerCleanupCreatedAt(value string) bool {
	created, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !created.IsZero() && created.UTC().Format(time.RFC3339Nano) == value
}

func cloneFactoryDockerCleanupBinding(binding factory.DockerCleanupBinding) factory.DockerCleanupBinding {
	binding.Labels = maps.Clone(binding.Labels)
	binding.VolumeOptions = maps.Clone(binding.VolumeOptions)
	return binding
}
