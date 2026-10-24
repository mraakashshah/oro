package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"unicode/utf8"
)

func boundedManagedDockerOutput(result HostResult) bool {
	return len(result.Stderr) <= 4096 && len(result.Stdout) <= (1<<20)-len(result.Stderr)
}

func parseManagedDockerVolumeInspection(result HostResult, expected DockerCleanupBinding) (DockerCleanupBinding, bool, error) {
	if !boundedManagedDockerOutput(result) {
		return DockerCleanupBinding{}, false, errors.New("managed Docker inspection exceeds output bounds")
	}
	if err := validateCleanupVolume(expected, DockerCleanupInspect); err != nil {
		return DockerCleanupBinding{}, false, err
	}
	if err := validateCleanupCreatedAt(expected.CreatedAt); err != nil {
		return DockerCleanupBinding{}, false, err
	}
	if managedDockerVolumeAbsent(result, expected.Name) {
		return cloneManagedDockerBinding(expected), false, nil
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 {
		return DockerCleanupBinding{}, false, errors.New("managed Docker inspection has uncertain status or diagnostics")
	}
	if err := validateDockerVolumeJSON(result.Stdout); err != nil {
		return DockerCleanupBinding{}, false, err
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(result.Stdout, &objects); err != nil {
		return DockerCleanupBinding{}, false, fmt.Errorf("decode managed Docker inspection: %w", err)
	}
	if len(objects) != 1 || objects[0] == nil {
		return DockerCleanupBinding{}, false, errors.New("managed Docker inspection requires exactly one object")
	}
	observed, err := decodeManagedDockerVolumeFields(objects[0], expected)
	if err != nil {
		return DockerCleanupBinding{}, false, err
	}
	comparison := expected
	if comparison.CreatedAt == "" {
		comparison.CreatedAt = observed.CreatedAt
	}
	if !reflect.DeepEqual(comparison, observed) {
		return DockerCleanupBinding{}, false, errors.New("managed Docker inspection differs from complete owned lifetime")
	}
	return observed, true, nil
}

func managedDockerVolumeAbsent(result HostResult, name string) bool {
	absent := "Error response from daemon: get " + name + ": no such volume\n"
	return result.ExitCode == 1 && bytes.Equal(result.Stdout, []byte("[]\n")) && bytes.Equal(result.Stderr, []byte(absent))
}

func cloneManagedDockerBinding(binding DockerCleanupBinding) DockerCleanupBinding {
	binding.Labels = maps.Clone(binding.Labels)
	binding.VolumeOptions = maps.Clone(binding.VolumeOptions)
	return binding
}

func decodeManagedDockerVolumeFields(fields map[string]json.RawMessage, expected DockerCleanupBinding) (DockerCleanupBinding, error) {
	observed := cloneManagedDockerBinding(expected)
	for _, field := range []struct {
		name  string
		value *string
	}{{"Name", &observed.Name}, {"CreatedAt", &observed.CreatedAt}, {"Driver", &observed.VolumeDriver}} {
		raw := fields[field.name]
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return DockerCleanupBinding{}, fmt.Errorf("managed Docker inspection is missing %s", field.name)
		}
		if err := json.Unmarshal(raw, field.value); err != nil {
			return DockerCleanupBinding{}, fmt.Errorf("decode managed Docker %s: %w", field.name, err)
		}
	}
	var err error
	observed.Labels, err = decodeManagedDockerVolumeMap(fields["Labels"])
	if err != nil {
		return DockerCleanupBinding{}, err
	}
	observed.VolumeOptions, err = decodeManagedDockerVolumeMap(fields["Options"])
	if err != nil {
		return DockerCleanupBinding{}, err
	}
	observed.CreatedAt, err = CanonicalDockerCreatedAt(observed.CreatedAt)
	if err != nil {
		return DockerCleanupBinding{}, fmt.Errorf("normalize managed Docker creation identity: %w", err)
	}
	return observed, nil
}

func decodeManagedDockerVolumeMap(raw json.RawMessage) (map[string]string, error) {
	var values map[string]*string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil || len(values) > 16 {
		return nil, errors.Join(errors.New("managed Docker ownership requires a bounded object"), err)
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		if value == nil {
			return nil, errors.New("managed Docker ownership requires string values")
		}
		result[key] = *value
	}
	if !boundedCleanupMap(result) {
		return nil, errors.New("managed Docker ownership exceeds descriptor bounds")
	}
	return result, nil
}

// Validate even ignored daemon fields before decoding required ownership. Every
// object rejects duplicate keys and >128 fields; nesting is bounded to 16 levels.
func validateDockerVolumeJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("managed Docker inspection requires valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkDockerVolumeJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("managed Docker inspection has trailing data")
	}
	return nil
}

func checkDockerVolumeJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("managed Docker inspection exceeds nesting bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read managed Docker JSON: %w", err)
	}
	switch token {
	case json.Delim('{'):
		if err := checkDockerVolumeJSONMembers(decoder, depth); err != nil {
			return err
		}
	case json.Delim('['):
		for decoder.More() {
			if err := checkDockerVolumeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("close managed Docker JSON: %w", err)
	}
	return nil
}

func checkDockerVolumeJSONMembers(decoder *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read managed Docker field: %w", err)
		}
		key, ok := token.(string)
		if !ok || seen[key] || len(seen) >= 128 {
			return errors.New("managed Docker inspection has duplicate or excessive fields")
		}
		seen[key] = true
		if err := checkDockerVolumeJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	return nil
}
