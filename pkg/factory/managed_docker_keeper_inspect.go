package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"
)

type managedKeeperInspection struct {
	ID                   string `json:"Id"`
	Name, Image, Created string
	Config               managedKeeperConfig
	State                managedKeeperState
	HostConfig           managedKeeperHostConfig
	Mounts               []managedKeeperMount
}
type managedKeeperConfig struct {
	User                         string
	Entrypoint, Cmd              []string
	Labels                       map[string]string
	Tty, OpenStdin, AttachStdout bool
}
type managedKeeperState struct {
	Status, Error                     string
	Running, Paused, Restarting, Dead bool
	Pid, ExitCode                     int
	StartedAt, FinishedAt             time.Time
}
type managedKeeperHostConfig struct {
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
	Binds, VolumesFrom      []string
	Tmpfs                   map[string]string
	Devices, DeviceRequests []json.RawMessage
	Mounts                  []managedKeeperMountConfig
}
type managedKeeperMountConfig struct {
	Type, Source, Target string
	ReadOnly             bool `keeper_optional:"true"`
	VolumeOptions        managedKeeperVolumeOptions
}
type (
	managedKeeperVolumeOptions struct{ NoCopy bool }
	managedKeeperMount         struct {
		Type, Name, Source, Destination, Driver string
		RW                                      bool
		Propagation                             string
	}
)

func parseManagedDockerKeeperInspection(result HostResult, expected managedDockerKeeperIntent, volume managedDockerVolumeIntent) (DockerCleanupBinding, error) {
	value, binding, err := parseManagedKeeperInspection(result, expected, volume)
	if err != nil {
		return DockerCleanupBinding{}, err
	}
	if err := validateManagedKeeperStopped(value.State); err != nil {
		return DockerCleanupBinding{}, err
	}
	return binding, nil
}

func parseManagedDockerRunningKeeperInspection(result HostResult, keeper managedDockerKeeperIntent, volume managedDockerVolumeIntent, expectedStartedAt string) (string, error) {
	if keeper.Binding.CreatedAt == "" {
		return "", errors.New("running source requires its bound creation lifetime")
	}
	value, _, err := parseManagedKeeperInspection(result, keeper, volume)
	if err != nil {
		return "", err
	}
	if err := validateManagedKeeperRunning(value.State); err != nil {
		return "", err
	}
	// Preserve the raw lexeme: time.Time decoding alone accepts overprecision and
	// other spellings that cannot identify a canonical source lifetime.
	var raw []struct{ State struct{ StartedAt string } }
	if err := json.Unmarshal(result.Stdout, &raw); err != nil {
		return "", fmt.Errorf("read raw keeper start time: %w", err)
	}
	if len(raw) != 1 {
		return "", errors.New("keeper inspection requires one object")
	}
	started, err := CanonicalDockerCreatedAt(raw[0].State.StartedAt)
	if err != nil {
		return "", fmt.Errorf("normalize keeper start time: %w", err)
	}
	if expectedStartedAt != "" && started != expectedStartedAt {
		return "", errors.New("keeper source continuity changed")
	}
	return started, nil
}

func validateManagedKeeperRunning(s managedKeeperState) error {
	if s.Status != "running" || !s.Running || s.Pid <= 1 || s.Paused || s.Restarting || s.Dead || s.ExitCode != 0 || s.Error != "" || !s.FinishedAt.IsZero() {
		return errors.New("source keeper is not an intact running guard")
	}
	return nil
}

func parseManagedKeeperInspection(result HostResult, expected managedDockerKeeperIntent, volume managedDockerVolumeIntent) (managedKeeperInspection, DockerCleanupBinding, error) {
	if !boundedManagedDockerOutput(result) || result.ExitCode != 0 || len(result.Stderr) != 0 {
		return managedKeeperInspection{}, DockerCleanupBinding{}, errors.New("keeper inspection has uncertain status or exceeds output bounds")
	}
	if err := validateManagedKeeperInspectionInput(expected, volume); err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, err
	}
	if err := validateDockerVolumeJSON(result.Stdout); err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, err
	}
	var objects []json.RawMessage
	if err := json.Unmarshal(result.Stdout, &objects); err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, fmt.Errorf("decode keeper inspection: %w", err)
	}
	if len(objects) != 1 {
		return managedKeeperInspection{}, DockerCleanupBinding{}, errors.New("keeper inspection requires one object")
	}
	if err := validateManagedGuardNamespaces(objects[0]); err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, fmt.Errorf("keeper namespaces: %w", err)
	}
	var value managedKeeperInspection
	if err := decodeManagedKeeperObject(objects[0], reflect.ValueOf(&value).Elem()); err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, err
	}
	if err := validateManagedKeeperInspection(value, expected, volume); err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, err
	}
	created, err := CanonicalDockerCreatedAt(value.Created)
	if err != nil {
		return managedKeeperInspection{}, DockerCleanupBinding{}, fmt.Errorf("normalize keeper creation time: %w", err)
	}
	if expected.Binding.CreatedAt != "" && expected.Binding.CreatedAt != created {
		return managedKeeperInspection{}, DockerCleanupBinding{}, errors.New("keeper creation lifetime changed")
	}
	binding := cloneManagedDockerBinding(expected.Binding)
	binding.CreatedAt = created
	binding.Labels = maps.Clone(value.Config.Labels)
	return value, binding, nil
}

// Every selected field must be explicitly present with its exact spelling.
// Unknown daemon fields remain allowed, but aliases cannot override ownership.
func decodeManagedKeeperObject(raw json.RawMessage, target reflect.Value) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.Join(errors.New("keeper requires a non-null object"), err)
	}
	if err := validateManagedKeeperFieldNames(fields, target.Type()); err != nil {
		return err
	}
	for i := 0; i < target.NumField(); i++ {
		field := target.Type().Field(i)
		name := managedKeeperFieldName(field)
		data, ok := fields[name]
		if !ok {
			if field.Tag.Get("keeper_optional") == "true" {
				continue
			}
			return fmt.Errorf("keeper inspection lacks %s", name)
		}
		if err := decodeManagedKeeperValue(data, target.Field(i)); err != nil {
			return fmt.Errorf("decode keeper field %s: %w", name, err)
		}
	}
	return nil
}

func managedKeeperFieldName(field reflect.StructField) string {
	name := field.Tag.Get("json")
	if name == "" {
		return field.Name
	}
	return name
}

func validateManagedKeeperFieldNames(fields map[string]json.RawMessage, kind reflect.Type) error {
	exact := kind == reflect.TypeFor[managedKeeperMountConfig]() || kind == reflect.TypeFor[managedKeeperVolumeOptions]()
	for key := range fields {
		found := false
		for i := 0; i < kind.NumField(); i++ {
			name := managedKeeperFieldName(kind.Field(i))
			if key == name {
				found = true
			} else if strings.EqualFold(key, name) {
				return errors.New("keeper inspection contains a field alias")
			}
		}
		if exact && !found {
			return errors.New("keeper mount contains an unadmitted option")
		}
	}
	return nil
}

func decodeManagedKeeperValue(data json.RawMessage, value reflect.Value) error {
	if value.Kind() == reflect.Struct && value.Type() != reflect.TypeFor[time.Time]() {
		return decodeManagedKeeperObject(data, value)
	}
	if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Struct {
		var elements []json.RawMessage
		if err := json.Unmarshal(data, &elements); err != nil {
			return fmt.Errorf("decode keeper array: %w", err)
		}
		value.Set(reflect.MakeSlice(value.Type(), len(elements), len(elements)))
		for i, element := range elements {
			if err := decodeManagedKeeperObject(element, value.Index(i)); err != nil {
				return err
			}
		}
		return nil
	}
	if bytes.Equal(data, []byte("null")) && value.Kind() != reflect.Map && value.Kind() != reflect.Slice {
		return errors.New("keeper inspection has a null scalar")
	}
	if value.Type() == reflect.TypeFor[map[string]string]() {
		if err := validateManagedKeeperStringMap(data); err != nil {
			return err
		}
	}
	if err := json.Unmarshal(data, value.Addr().Interface()); err != nil {
		return fmt.Errorf("decode keeper value: %w", err)
	}
	return nil
}

func validateManagedKeeperStringMap(data json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode keeper ownership map: %w", err)
	}
	for _, raw := range fields {
		if bytes.Equal(raw, []byte("null")) {
			return errors.New("keeper ownership map requires string values")
		}
	}
	return nil
}

func validateManagedKeeperInspection(v managedKeeperInspection, intent managedDockerKeeperIntent, volume managedDockerVolumeIntent) error {
	if v.ID != intent.Binding.ContainerID || v.Name != "/"+intent.Keeper.Name || v.Image != intent.Binding.ImageID {
		return errors.New("keeper exact container identity changed")
	}
	if err := validateManagedKeeperGuard(v.Config, intent); err != nil {
		return err
	}
	if err := validateManagedKeeperHardening(v.HostConfig); err != nil {
		return err
	}
	return validateManagedKeeperMounts(v, volume.Binding.Name)
}

func validateManagedKeeperHardening(h managedKeeperHostConfig) error {
	if h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.CgroupnsMode != "private" || !h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || !reflect.DeepEqual(h.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(h.SecurityOpt, []string{"no-new-privileges"}) {
		return errors.New("keeper namespace or security configuration differs")
	}
	return validateManagedKeeperLimits(h)
}

func validateManagedKeeperLimits(h managedKeeperHostConfig) error {
	if h.Memory != 64<<20 || h.MemorySwap != 64<<20 || h.NanoCpus != 250000000 || h.PidsLimit != 16 || h.ShmSize != 1<<20 {
		return errors.New("keeper fixed resource limits differ")
	}
	return validateManagedKeeperExtras(h)
}

func validateManagedKeeperExtras(h managedKeeperHostConfig) error {
	if h.RestartPolicy.Name != "no" || h.RestartPolicy.MaximumRetryCount != 0 || h.LogConfig.Type != "none" || len(h.LogConfig.Config) != 0 || len(h.Binds) != 0 || len(h.VolumesFrom) != 0 || len(h.Tmpfs) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 {
		return errors.New("keeper has an extra device, mount, restart or logging configuration")
	}
	return nil
}

func validateManagedKeeperMounts(v managedKeeperInspection, volumeName string) error {
	if len(v.Mounts) != 1 || len(v.HostConfig.Mounts) != 1 {
		return errors.New("keeper requires exactly one source volume mount")
	}
	actual := v.Mounts[0]
	configured := v.HostConfig.Mounts[0]
	if actual.Type != "volume" || actual.Name != volumeName || actual.Destination != "/source" || actual.Driver != "local" || !actual.RW || actual.Propagation != "" || actual.Source == "" {
		return errors.New("keeper resolved source mount differs")
	}
	if configured.Type != "volume" || configured.Source != volumeName || configured.Target != "/source" || configured.ReadOnly || !configured.VolumeOptions.NoCopy {
		return errors.New("keeper source mount configuration differs")
	}
	return nil
}

func validateManagedKeeperGuard(c managedKeeperConfig, intent managedDockerKeeperIntent) error {
	if c.User != "65534:65534" || !reflect.DeepEqual(c.Entrypoint, []string{"/usr/local/bin/oro-factory-guard"}) || !reflect.DeepEqual(c.Cmd, []string{"--deadline", intent.GuardDeadline.Format(time.RFC3339Nano)}) || c.Tty || c.OpenStdin || !c.AttachStdout {
		return errors.New("keeper guard configuration differs")
	}
	return validateManagedKeeperObservedLabels(intent.Binding, c.Labels)
}

func validateManagedKeeperStopped(s managedKeeperState) error {
	if s.Status != "created" || s.Running || s.Paused || s.Restarting || s.Dead || s.Pid != 0 || s.ExitCode != 0 || s.Error != "" || !s.StartedAt.IsZero() || !s.FinishedAt.IsZero() {
		return errors.New("keeper must be never-started and stopped")
	}
	return nil
}

func validateManagedKeeperInspectionInput(expected managedDockerKeeperIntent, volume managedDockerVolumeIntent) error {
	if expected.Phase != "acknowledged" || !managedKeeperID(expected.Binding.ContainerID) || expected.Volume != volume.Volume || volume.Phase != "acknowledged" || volume.Binding.CreatedAt == "" {
		return errors.New("keeper inspection lacks acknowledged exact source identity")
	}
	return nil
}
