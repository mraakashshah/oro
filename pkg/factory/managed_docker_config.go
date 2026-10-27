package factory

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ManagedDockerConfig is the immutable scalar result of the trusted artifact
// reader. It carries identities, not authority to initialize or grant a scope.
// DockerPath and OroPath are verified artifact inputs, never additional copies.
type ManagedDockerConfig struct {
	StateRoot, ObjectiveID, PolicyHash     string
	Deadline, HardStop                     time.Time
	RuntimeConfigPath, RuntimeConfigSHA256 string
	OroPath, OroSHA256                     string
	DockerPath, DockerSHA256, DockerConfig string
	DockerContext, DockerEndpoint          string
	ModelHelper                            *ModelAdapterConfig
}

// ManagedDockerConfigReader reopens and verifies the exact frozen artifact,
// private context snapshot, original Oro/Docker executables and dedicated endpoint.
// CLI owns those filesystem checks; core binds the view to committed admission.
type ManagedDockerConfigReader func(context.Context) (ManagedDockerConfig, error)

func validateManagedDockerConfig(config ManagedDockerConfig, manifest managedManifest) error {
	root, id := manifest.Reservation.StateRoot, manifest.Spec.ID
	if config.StateRoot != root || config.ObjectiveID != id || config.PolicyHash != manifest.PolicyHash || config.DockerContext != manifest.Spec.Runtime.DockerContext ||
		config.Deadline != manifest.Deadline || config.HardStop != manifest.HardStop {
		return errors.New("managed Docker config differs from immutable admission")
	}
	directory := filepath.Join(root, id, "runtime")
	if config.RuntimeConfigPath != filepath.Join(directory, "runtime.json") || config.DockerConfig != filepath.Join(directory, "docker") {
		return errors.New("managed Docker runtime paths differ from admitted layout")
	}
	for _, path := range []string{config.StateRoot, config.RuntimeConfigPath, config.OroPath, config.DockerPath, config.DockerConfig} {
		if !managedAbsolutePath(path) {
			return errors.New("managed Docker requires canonical absolute paths")
		}
	}
	for _, digest := range []string{config.PolicyHash, config.RuntimeConfigSHA256, config.OroSHA256, config.DockerSHA256} {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
			return errors.New("managed Docker requires exact SHA256 identities")
		}
	}
	return validateManagedDockerEndpoint(config.DockerEndpoint)
}

func validateManagedDockerEndpoint(value string) error {
	endpoint := strings.TrimPrefix(value, "unix://")
	if !strings.HasPrefix(value, "unix://") || !managedAbsolutePath(endpoint) || strings.ContainsAny(endpoint, " ?#%") {
		return errors.New("managed Docker requires an exact local Unix endpoint")
	}
	return nil
}
