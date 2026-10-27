package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"oro/pkg/factory"
)

const (
	factoryRuntimeConfigMaxBytes  = 64 << 10
	factoryRuntimeContextMaxBytes = 8 << 10
	factoryRuntimeBundleMaxBytes  = 1 << 20
	// Three files and five directories, including runtime itself. Reserve 64 KiB
	// of filesystem/path metadata per entry, in addition to actual file bytes.
	factoryRuntimeBundleMetadataBytes = 8 * (64 << 10)
)

// factoryRuntimeConfig is the canonical producer/consumer contract. CLI start
// must populate it from a verified immutable admission view and freeze its hash
// before launching services. Reading it authenticates the supplied hash, not
// current journal admission: callers must compare the trusted admission view.
// DockerPath names the original admitted binary, never a fourth executable copy.
type factoryRuntimeConfig struct {
	Version               int                         `json:"version"`
	StateRoot             string                      `json:"state_root"`
	ObjectiveID           string                      `json:"objective_id"`
	PolicyHash            string                      `json:"policy_hash"`
	OroPath               string                      `json:"oro_path"`
	OroSHA256             string                      `json:"oro_sha256"`
	GitPath               string                      `json:"git_path"`
	GitSHA256             string                      `json:"git_sha256"`
	Repository            string                      `json:"repository"`
	TargetRef             string                      `json:"target_ref"`
	Deadline              time.Time                   `json:"deadline"`
	HardStop              time.Time                   `json:"hard_stop"`
	CleanupTimeoutSeconds int                         `json:"cleanup_timeout_seconds"`
	DockerPath            string                      `json:"docker_path"`
	DockerSHA256          string                      `json:"docker_sha256"`
	DockerContext         string                      `json:"docker_context"`
	DockerEndpoint        string                      `json:"docker_endpoint"`
	ModelHelper           *factory.ModelAdapterConfig `json:"model_helper,omitempty"`
}

type factoryDockerCleanupConfig struct {
	factoryRuntimeConfig
	RuntimeConfigPath   string
	RuntimeConfigSHA256 string
	DockerConfig        string
}

func canonicalFactoryRuntimeConfig(config factoryRuntimeConfig) ([]byte, error) {
	if err := validateFactoryRuntimeConfigFields(config); err != nil {
		return nil, err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode factory runtime config: %w", err)
	}
	if len(data) > factoryRuntimeConfigMaxBytes {
		return nil, errors.New("factory runtime config exceeds 64 KiB")
	}
	return data, nil
}

func validateFactoryRuntimeConfigFields(config factoryRuntimeConfig) error {
	if config.Version != 1 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(config.ObjectiveID) ||
		!factoryObserverRef(config.TargetRef) {
		return errors.New("factory runtime config has an invalid version, objective or target")
	}
	for _, digest := range []string{config.PolicyHash, config.OroSHA256, config.GitSHA256, config.DockerSHA256} {
		if !factoryJobLowerHex(digest, 64) {
			return errors.New("factory runtime config requires exact lowercase SHA-256 identities")
		}
	}
	for _, path := range []string{config.StateRoot, config.Repository, config.OroPath, config.GitPath, config.DockerPath} {
		if !factoryJobAbsolutePath(path) {
			return errors.New("factory runtime config requires canonical absolute paths")
		}
	}
	if err := validateFactoryModelConfig(config); err != nil {
		return err
	}
	if err := validateFactoryRuntimeTimes(config); err != nil {
		return err
	}
	if config.DockerContext == "default" || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`).MatchString(config.DockerContext) {
		return errors.New("factory runtime requires an explicit dedicated Docker context")
	}
	path := strings.TrimPrefix(config.DockerEndpoint, "unix://")
	if !strings.HasPrefix(config.DockerEndpoint, "unix://") || !factoryJobAbsolutePath(path) || strings.ContainsAny(path, " ?#%") {
		return errors.New("factory runtime requires a canonical local Unix Docker endpoint")
	}
	return nil
}

func validateFactoryRuntimeTimes(config factoryRuntimeConfig) error {
	for _, deadline := range []time.Time{config.Deadline, config.HardStop} {
		if deadline.IsZero() || deadline.Location() != time.UTC || deadline.Year() < 1 || deadline.Year() > 9999 {
			return errors.New("factory runtime requires original canonical UTC deadlines")
		}
	}
	seconds := config.CleanupTimeoutSeconds
	if seconds <= 0 || int64(seconds) > math.MaxInt64/int64(time.Second) ||
		!config.HardStop.After(config.Deadline) || config.Deadline.Add(time.Duration(seconds)*time.Second).After(config.HardStop) {
		return errors.New("factory runtime cleanup allowance exceeds its original hard stop")
	}
	// Cleanup deliberately remains readable after the original work deadline.
	// Active scope authority, not this artifact codec, checks current time.
	return nil
}

type factoryRuntimeDockerMetadata struct {
	Name      string                        `json:"Name"`
	Metadata  struct{}                      `json:"Metadata"`
	Endpoints factoryRuntimeDockerEndpoints `json:"Endpoints"`
}

type factoryRuntimeDockerEndpoints struct {
	Docker factoryRuntimeDockerEndpoint `json:"docker"`
}

type factoryRuntimeDockerEndpoint struct {
	Host          string `json:"Host"`
	SkipTLSVerify bool   `json:"SkipTLSVerify"`
}

// factoryDockerContextSnapshot produces exactly the two read-only files the
// reader accepts. A publisher may retain one final bundle and one staging bundle
// only; each (including metadata) fits ManagedHostRetention's 1 MiB allowance.
// Publication and its two-copy lifecycle are owned by CLI start, not this reader.
func factoryDockerContextSnapshot(config factoryRuntimeConfig) (configJSON, contextJSON []byte, err error) {
	runtimeBytes, err := canonicalFactoryRuntimeConfig(config)
	if err != nil {
		return nil, nil, err
	}
	metadata := factoryRuntimeDockerMetadata{
		Name:      config.DockerContext,
		Endpoints: factoryRuntimeDockerEndpoints{Docker: factoryRuntimeDockerEndpoint{Host: config.DockerEndpoint}},
	}
	meta, err := json.Marshal(metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("encode factory Docker context: %w", err)
	}
	if len(meta) > factoryRuntimeContextMaxBytes ||
		len(runtimeBytes)+len(meta)+2+factoryRuntimeBundleMetadataBytes > factoryRuntimeBundleMaxBytes {
		return nil, nil, errors.New("factory runtime snapshot exceeds its bounded bundle allowance")
	}
	return []byte("{}"), meta, nil
}

func factoryRuntimeConfigPath(config factoryRuntimeConfig) string {
	return filepath.Join(config.StateRoot, config.ObjectiveID, "runtime", "runtime.json")
}

func validateFactoryModelCredentialPath(path, root, repository string) error {
	if !utf8.ValidString(path) || len(path) > 4096 || !factoryJobAbsolutePath(path) || strings.ContainsRune(path, 0) {
		return errors.New("model credential reference must be an absolute canonical path")
	}
	for _, parent := range []string{root, repository} {
		rel, err := filepath.Rel(parent, path)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return errors.New("model credential reference must be outside state and repository")
		}
	}
	return nil
}

func validateFactoryModelConfig(config factoryRuntimeConfig) error {
	h := config.ModelHelper
	if h == nil {
		return nil
	}
	if h.Protocol != "oro-terra-responses-v1" || h.HelperSHA256 != config.OroSHA256 {
		return errors.New("invalid model helper identity")
	}
	return validateFactoryModelCredentialPath(h.CredentialPath, config.StateRoot, config.Repository)
}
