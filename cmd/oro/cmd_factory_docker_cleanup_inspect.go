package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"oro/pkg/factory"
)

const factoryDockerCleanupInspectMaxBytes = 1 << 20

// Missing-object responses are observations only. They do not prove that an
// earlier daemon request drained, acknowledge a mutation, or retire any scope.
func parseFactoryDockerCleanupInspection(permit factory.DockerCleanupPermit, result factory.HostResult) (factory.DockerCleanupBinding, string, error) {
	args, err := factoryDockerCleanupInspectArgs(permit)
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	if len(result.Stderr) > 4096 || len(result.Stdout) > factoryDockerCleanupInspectMaxBytes-len(result.Stderr) {
		return factory.DockerCleanupBinding{}, "", errors.New("cleanup inspection exceeds its output bound")
	}
	if factoryDockerCleanupAbsent(permit.Binding.Kind, args[len(args)-1], result) {
		return cloneFactoryDockerCleanupBinding(permit.Binding), "observed_absent", nil
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 {
		return factory.DockerCleanupBinding{}, "", errors.New("cleanup inspection failed or produced uncertain diagnostics")
	}
	var objects []json.RawMessage
	if err := json.Unmarshal(result.Stdout, &objects); err != nil {
		return factory.DockerCleanupBinding{}, "", fmt.Errorf("decode cleanup inspection: %w", err)
	}
	if len(objects) != 1 {
		return factory.DockerCleanupBinding{}, "", errors.New("cleanup inspection requires exactly one object")
	}
	fields, err := factoryDockerCleanupFields(objects[0])
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	if permit.Binding.Kind == "volume" {
		return parseFactoryDockerCleanupVolume(permit, fields)
	}
	return parseFactoryDockerCleanupContainer(permit, fields)
}

func factoryDockerCleanupAbsent(kind, identity string, result factory.HostResult) bool {
	message := "Error response from daemon: No such container: " + identity + "\n"
	if kind == "volume" {
		message = "Error response from daemon: get " + identity + ": no such volume\n"
	}
	// These exact exit/stdout/stderr forms were probed with the admitted Docker
	// CLI, explicit private config and dedicated context; no substring matching.
	return result.ExitCode == 1 && bytes.Equal(result.Stdout, []byte("[]\n")) && bytes.Equal(result.Stderr, []byte(message))
}

func factoryDockerCleanupFields(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("cleanup inspection requires an object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read cleanup inspection key: %w", err)
		}
		key, ok := token.(string)
		if !ok || fields[key] != nil || len(fields) >= 128 {
			return nil, errors.New("cleanup inspection has duplicate or excessive fields")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode cleanup inspection value: %w", err)
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("close cleanup inspection object: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("cleanup inspection contains trailing object data")
	}
	return fields, nil
}

type factoryDockerCleanupField struct {
	name  string
	value any
}

func decodeFactoryDockerCleanupFields(fields map[string]json.RawMessage, values []factoryDockerCleanupField) error {
	for _, field := range values {
		data := fields[field.name]
		if len(data) == 0 || bytes.Equal(data, []byte("null")) {
			return fmt.Errorf("cleanup inspection is missing %s", field.name)
		}
		if err := json.Unmarshal(data, field.value); err != nil {
			return fmt.Errorf("decode cleanup inspection %s: %w", field.name, err)
		}
	}
	return nil
}

func parseFactoryDockerCleanupContainer(permit factory.DockerCleanupPermit, fields map[string]json.RawMessage) (factory.DockerCleanupBinding, string, error) {
	observed := cloneFactoryDockerCleanupBinding(permit.Binding)
	var name string
	if err := decodeFactoryDockerCleanupFields(fields, []factoryDockerCleanupField{
		{"Id", &observed.ContainerID}, {"Name", &name}, {"Created", &observed.CreatedAt}, {"Image", &observed.ImageID},
	}); err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	if name != "/"+permit.Binding.Name || !factoryJobLowerHex(observed.ContainerID, 64) {
		return factory.DockerCleanupBinding{}, "", errors.New("cleanup inspection changed exact container identity")
	}
	config, err := factoryDockerCleanupFields(fields["Config"])
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	observed.Labels, err = parseFactoryDockerCleanupMap(config["Labels"])
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	if err := matchFactoryDockerCleanupBinding(permit, observed); err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	state, err := parseFactoryDockerCleanupState(fields["State"])
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	return observed, state, nil
}

func parseFactoryDockerCleanupVolume(permit factory.DockerCleanupPermit, fields map[string]json.RawMessage) (factory.DockerCleanupBinding, string, error) {
	observed := cloneFactoryDockerCleanupBinding(permit.Binding)
	if err := decodeFactoryDockerCleanupFields(fields, []factoryDockerCleanupField{
		{"Name", &observed.Name}, {"CreatedAt", &observed.CreatedAt}, {"Driver", &observed.VolumeDriver},
	}); err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	var err error
	observed.Labels, err = parseFactoryDockerCleanupMap(fields["Labels"])
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	observed.VolumeOptions, err = parseFactoryDockerCleanupMap(fields["Options"])
	if err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	if err := matchFactoryDockerCleanupBinding(permit, observed); err != nil {
		return factory.DockerCleanupBinding{}, "", err
	}
	return observed, "present", nil
}

func parseFactoryDockerCleanupMap(data []byte) (map[string]string, error) {
	fields, err := factoryDockerCleanupFields(data)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(fields))
	for key, raw := range fields {
		var value string
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("cleanup ownership map requires string values")
		}
		values[key] = value
	}
	if !factoryDockerCleanupMapBound(values) {
		return nil, errors.New("cleanup ownership map exceeds its descriptor bound")
	}
	return values, nil
}

func matchFactoryDockerCleanupBinding(permit factory.DockerCleanupPermit, observed factory.DockerCleanupBinding) error {
	if !factoryDockerCleanupCreatedAt(observed.CreatedAt) {
		return errors.New("cleanup inspection requires canonical creation identity")
	}
	expected := permit.Binding
	if permit.Operation == factory.DockerCleanupInspect && expected.CreatedAt == "" {
		expected.CreatedAt = observed.CreatedAt
		if expected.Kind == "container" {
			expected.ContainerID = observed.ContainerID
		}
	}
	if !reflect.DeepEqual(expected, observed) {
		return errors.New("cleanup inspection differs from the complete admitted binding")
	}
	return nil
}

type factoryDockerCleanupStateFlags struct {
	running, paused, restarting, dead bool
}

func parseFactoryDockerCleanupState(data []byte) (string, error) {
	fields, err := factoryDockerCleanupFields(data)
	if err != nil {
		return "", err
	}
	var status string
	var flags factoryDockerCleanupStateFlags
	var pid int
	if err := decodeFactoryDockerCleanupFields(fields, []factoryDockerCleanupField{
		{"Status", &status}, {"Running", &flags.running}, {"Pid", &pid}, {"Paused", &flags.paused}, {"Restarting", &flags.restarting}, {"Dead", &flags.dead},
	}); err != nil {
		return "", err
	}
	states := map[string]struct {
		flags       factoryDockerCleanupStateFlags
		livePID     bool
		observation string
	}{
		"running":    {flags: factoryDockerCleanupStateFlags{running: true}, livePID: true, observation: "present"},
		"paused":     {flags: factoryDockerCleanupStateFlags{running: true, paused: true}, livePID: true, observation: "present"},
		"restarting": {flags: factoryDockerCleanupStateFlags{running: true, restarting: true}, observation: "present"},
		"created":    {observation: "stopped"},
		"exited":     {observation: "stopped"},
		"dead":       {flags: factoryDockerCleanupStateFlags{dead: true}, observation: "stopped"},
	}
	expected, ok := states[status]
	if !ok || pid < 0 || expected.livePID != (pid > 0) || flags != expected.flags {
		return "", errors.New("cleanup inspection state is contradictory or remains uncertain")
	}
	return expected.observation, nil
}
