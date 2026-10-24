package factory

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

const dockerCleanupResultBytes = 16 << 10

// DockerCleanupResult reports an exact observation after one cleanup helper.
// Neither absence nor acknowledged mutation proves daemon request drain or scope
// quiescence. Callers must authenticate the permit independently of this codec.
type DockerCleanupResult struct {
	Version              int                  `json:"version"`
	PermitSHA256         string               `json:"permit_sha256"`
	Scope                ScopeHandle          `json:"scope"`
	Target               ScopeHandle          `json:"target"`
	Operation            DockerCleanupOp      `json:"operation"`
	Binding              DockerCleanupBinding `json:"binding"`
	Observation          string               `json:"observation"`
	MutationAcknowledged bool                 `json:"mutation_acknowledged"`
}

// EncodeDockerCleanupResult encodes an observation for an already-authenticated
// permit. It neither grants authority nor checks current journal/time state.
func EncodeDockerCleanupResult(result DockerCleanupResult, trustedPermit DockerCleanupPermit) ([]byte, error) {
	if err := validateCleanupResultIdentity(result, trustedPermit); err != nil {
		return nil, err
	}
	if err := validateCleanupResultObservation(result); err != nil {
		return nil, err
	}
	if err := validateCleanupResultBinding(result, trustedPermit); err != nil {
		return nil, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode cleanup result: %w", err)
	}
	if len(data) > dockerCleanupResultBytes {
		return nil, errors.New("cleanup result exceeds its 16 KiB bound")
	}
	return data, nil
}

// DecodeDockerCleanupResult requires exact canonical JSON and a trusted permit.
func DecodeDockerCleanupResult(data []byte, trustedPermit DockerCleanupPermit) (DockerCleanupResult, error) {
	if len(data) > dockerCleanupResultBytes {
		return DockerCleanupResult{}, errors.New("cleanup result exceeds its 16 KiB bound")
	}
	var result DockerCleanupResult
	if err := json.Unmarshal(data, &result); err != nil {
		return DockerCleanupResult{}, fmt.Errorf("decode cleanup result: %w", err)
	}
	canonical, err := EncodeDockerCleanupResult(result, trustedPermit)
	if err != nil {
		return DockerCleanupResult{}, err
	}
	if !bytes.Equal(data, canonical) {
		return DockerCleanupResult{}, errors.New("cleanup result is not exact canonical JSON")
	}
	return result, nil
}

func validateCleanupResultIdentity(result DockerCleanupResult, permit DockerCleanupPermit) error {
	if result.Version != 1 || result.Scope != permit.Scope || result.Target != permit.Target || result.Operation != permit.Operation {
		return errors.New("cleanup result differs from its exact version or permit identity")
	}
	canonical, err := json.Marshal(permit)
	if err != nil {
		return fmt.Errorf("encode trusted cleanup permit identity: %w", err)
	}
	if len(canonical) > ManagedCleanupPermitBytes || result.PermitSHA256 != contentHash(canonical) {
		return errors.New("cleanup result does not bind the bounded canonical permit")
	}
	return nil
}

// Successful observations are deliberately narrower than possible Docker states:
// inspect: present/stopped/observed_absent, acknowledgment false;
// stop: stopped with acknowledgment true, or observed_absent with either value;
// remove: observed_absent with either value. Volumes never use stopped or stop.
// A failed mutation/postcondition is a helper error, not a successful result.
// The producer alone proves that acknowledgment true follows mutation exit zero.
func validateCleanupResultObservation(result DockerCleanupResult) error {
	if result.Binding.Kind != "container" && result.Binding.Kind != "volume" {
		return errors.New("cleanup result has an invalid resource kind")
	}
	observation, ack := result.Observation, result.MutationAcknowledged
	if observation != "present" && observation != "stopped" && observation != "observed_absent" {
		return errors.New("cleanup result has an invalid observation")
	}
	var valid bool
	switch result.Operation {
	case DockerCleanupInspect:
		valid = !ack && (result.Binding.Kind != "volume" || observation != "stopped")
	case DockerCleanupStop:
		valid = result.Binding.Kind == "container" && (observation == "observed_absent" || (observation == "stopped" && ack))
	case DockerCleanupRemove:
		valid = observation == "observed_absent"
	}
	if !valid {
		return errors.New("cleanup operation, observation and mutation acknowledgment are incompatible")
	}
	return nil
}

func validateCleanupResultBinding(result DockerCleanupResult, permit DockerCleanupPermit) error {
	observed := result.Binding
	if result.Observation != "observed_absent" {
		if err := validateCleanupResultLifetime(observed); err != nil {
			return err
		}
		// Inspection can discover only an unknown lifetime. Containers fill their
		// full ID and CreatedAt; volumes fill only CreatedAt. Absence must echo the
		// original binding, and a known identity is never replaced or erased.
		if permit.Operation == DockerCleanupInspect && permit.Binding.CreatedAt == "" && permit.Binding.ContainerID == "" {
			observed.ContainerID, observed.CreatedAt = "", ""
		}
	}
	if !reflect.DeepEqual(observed, permit.Binding) {
		return errors.New("cleanup observation changed its immutable resource binding")
	}
	return nil
}

func validateCleanupResultLifetime(binding DockerCleanupBinding) error {
	if binding.CreatedAt == "" || len(binding.CreatedAt) > 30 {
		return errors.New("present cleanup observation requires a canonical creation identity")
	}
	if err := validateCleanupCreatedAt(binding.CreatedAt); err != nil {
		return err
	}
	if binding.Kind == "volume" {
		if binding.ContainerID != "" {
			return errors.New("volume observation cannot have a container identity")
		}
		return nil
	}
	if len(binding.ContainerID) != 64 {
		return errors.New("present cleanup observation requires a full canonical container identity")
	}
	id, err := hex.DecodeString(binding.ContainerID)
	if err != nil || len(id) != 32 || hex.EncodeToString(id) != binding.ContainerID {
		return errors.New("present cleanup observation requires a full canonical container identity")
	}
	return nil
}
