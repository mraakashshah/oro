package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	storagepkg "oro/pkg/storage"

	"gopkg.in/yaml.v3"
)

const (
	defaultNamespaceStopBytes      int64 = 512 << 20
	defaultAggregateAdmissionBytes int64 = 3 << 30
)

var (
	// ErrPolicyWeakening reports an untrusted layer that attempts to increase a host limit.
	ErrPolicyWeakening = errors.New("storage policy weakening")
	// ErrUntrustedCleaner reports an attempt to configure a cleaner outside trusted user configuration.
	ErrUntrustedCleaner = storagepkg.ErrUntrustedCleaner
)

// PolicySource identifies the layer that set a resolved policy field.
type PolicySource string

const (
	// PolicySourceDefault identifies compiled safe defaults.
	PolicySourceDefault PolicySource = "default"
	// PolicySourceUser identifies trusted user configuration.
	PolicySourceUser PolicySource = "user"
	// PolicySourceProject identifies project configuration that tightened a policy.
	PolicySourceProject PolicySource = "project"
	// PolicySourceEnvironment identifies a trusted-user-allowlisted environment override.
	PolicySourceEnvironment PolicySource = "environment"
	// PolicySourceCLI identifies a safe invocation-local CLI override.
	PolicySourceCLI PolicySource = "cli"
)

// StorageCleaner is a fixed executable and argument vector authorized by trusted user configuration.
type StorageCleaner struct {
	Executable string
	Args       []string
	Trusted    bool
}

// StoragePolicyProvenance records the winning source for every policy limit.
type StoragePolicyProvenance struct {
	NamespaceStopBytes      PolicySource
	AggregateAdmissionBytes PolicySource
}

// StoragePolicy is the resolved storage policy snapshot shared by all execution modes.
type StoragePolicy struct {
	NamespaceStopBytes      int64
	AggregateAdmissionBytes int64
	DeletionRoots           []string
	Cleaners                []StorageCleaner
	Provenance              StoragePolicyProvenance
}

// Clone returns an independent copy of a policy snapshot.
func (p StoragePolicy) Clone() StoragePolicy {
	p.DeletionRoots = append([]string(nil), p.DeletionRoots...)
	p.Cleaners = append([]StorageCleaner(nil), p.Cleaners...)
	for i := range p.Cleaners {
		p.Cleaners[i].Args = append([]string(nil), p.Cleaners[i].Args...)
	}
	return p
}

// StoragePolicyOverrides contains the safe, invocation-local policy overrides.
// Zero values leave the corresponding policy field unchanged.
type StoragePolicyOverrides struct {
	NamespaceStopBytes int64
}

// StoragePolicySources explicitly identifies every policy input, enabling hermetic callers and tests.
type StoragePolicySources struct {
	UserConfigPath    string
	ProjectConfigPath string
	Environment       map[string]string
	CLI               StoragePolicyOverrides
}

type storagePolicyFile struct {
	NamespaceStopBytes      *int64               `yaml:"namespace_stop_bytes"`
	AggregateAdmissionBytes *int64               `yaml:"aggregate_admission_bytes"`
	DeletionRoots           []string             `yaml:"deletion_roots"`
	Cleaners                []storageCleanerFile `yaml:"cleaners"`
	Environment             map[string]bool      `yaml:"environment"`
}

type storageCleanerFile struct {
	Executable string   `yaml:"executable"`
	Args       []string `yaml:"args"`
}

// LoadStoragePolicy resolves one immutable storage policy using safe source precedence.
func LoadStoragePolicy(ctx context.Context, sources StoragePolicySources) (StoragePolicy, error) {
	if err := ctx.Err(); err != nil {
		return StoragePolicy{}, fmt.Errorf("storage policy context: %w", err)
	}

	policy := defaultStoragePolicy()
	user, err := loadStoragePolicyFile(userStorageConfigPath(sources))
	if err != nil {
		return StoragePolicy{}, err
	}
	if err := applyTrustedUserStoragePolicy(&policy, user); err != nil {
		return StoragePolicy{}, err
	}

	project, err := loadStoragePolicyFile(sources.ProjectConfigPath)
	if err != nil {
		return StoragePolicy{}, err
	}
	if err := applyProjectStoragePolicy(&policy, project); err != nil {
		return StoragePolicy{}, err
	}

	if err := applyEnvironmentStoragePolicy(&policy, user, sources.Environment); err != nil {
		return StoragePolicy{}, err
	}
	if err := applyCLIStoragePolicy(&policy, sources.CLI); err != nil {
		return StoragePolicy{}, err
	}
	return policy.Clone(), nil
}

func defaultStoragePolicy() StoragePolicy {
	return StoragePolicy{
		NamespaceStopBytes:      defaultNamespaceStopBytes,
		AggregateAdmissionBytes: defaultAggregateAdmissionBytes,
		Provenance: StoragePolicyProvenance{
			NamespaceStopBytes:      PolicySourceDefault,
			AggregateAdmissionBytes: PolicySourceDefault,
		},
	}
}

func userStorageConfigPath(sources StoragePolicySources) string {
	if sources.UserConfigPath != "" {
		return sources.UserConfigPath
	}
	if oroHome := os.Getenv("ORO_HOME"); oroHome != "" {
		return filepath.Join(oroHome, "config.yaml")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".oro", "config.yaml")
	}
	return ""
}

func loadStoragePolicyFile(path string) (*storagePolicyFile, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // policy source path is explicit caller input
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read storage policy %s: %w", path, err)
	}
	var document configFile
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse storage policy %s: %w", path, err)
	}
	return document.Storage, nil
}

func applyTrustedUserStoragePolicy(policy *StoragePolicy, source *storagePolicyFile) error {
	if source == nil {
		return nil
	}
	if err := applyStorageLimits(policy, source, PolicySourceUser, false); err != nil {
		return err
	}
	for _, root := range source.DeletionRoots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("invalid empty storage deletion root")
		}
		policy.DeletionRoots = append(policy.DeletionRoots, root)
	}
	for _, cleaner := range source.Cleaners {
		if strings.TrimSpace(cleaner.Executable) == "" {
			return fmt.Errorf("invalid storage cleaner")
		}
		policy.Cleaners = append(policy.Cleaners, StorageCleaner{
			Executable: cleaner.Executable,
			Args:       append([]string(nil), cleaner.Args...),
			Trusted:    true,
		})
	}
	return nil
}

func applyProjectStoragePolicy(policy *StoragePolicy, source *storagePolicyFile) error {
	if source == nil {
		return nil
	}
	if len(source.Cleaners) != 0 {
		return fmt.Errorf("project storage cleaners: %w", ErrUntrustedCleaner)
	}
	if len(source.DeletionRoots) != 0 {
		return fmt.Errorf("project storage deletion roots: %w", ErrPolicyWeakening)
	}
	return applyStorageLimits(policy, source, PolicySourceProject, true)
}

func applyStorageLimits(policy *StoragePolicy, source *storagePolicyFile, provenance PolicySource, tightenOnly bool) error {
	if source.NamespaceStopBytes != nil {
		if err := applyStorageLimit(&policy.NamespaceStopBytes, *source.NamespaceStopBytes, policy.Provenance.NamespaceStopBytes, provenance, tightenOnly, "namespace_stop_bytes"); err != nil {
			return err
		}
		policy.Provenance.NamespaceStopBytes = provenance
	}
	if source.AggregateAdmissionBytes != nil {
		if err := applyStorageLimit(&policy.AggregateAdmissionBytes, *source.AggregateAdmissionBytes, policy.Provenance.AggregateAdmissionBytes, provenance, tightenOnly, "aggregate_admission_bytes"); err != nil {
			return err
		}
		policy.Provenance.AggregateAdmissionBytes = provenance
	}
	return nil
}

func applyStorageLimit(current *int64, value int64, currentSource, nextSource PolicySource, tightenOnly bool, field string) error {
	if value <= 0 {
		return fmt.Errorf("storage policy %s must be positive", field)
	}
	if tightenOnly && value > *current {
		return fmt.Errorf("storage policy %s from %s exceeds %s value: %w", field, nextSource, currentSource, ErrPolicyWeakening)
	}
	*current = value
	return nil
}

func applyEnvironmentStoragePolicy(policy *StoragePolicy, user *storagePolicyFile, environment map[string]string) error {
	if user == nil || len(user.Environment) == 0 {
		return nil
	}
	for name, enabled := range user.Environment {
		if !enabled {
			continue
		}
		value, ok := environment[name]
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("parse storage environment %s: %w", name, err)
		}
		switch name {
		case "ORO_STORAGE_NAMESPACE_STOP_BYTES":
			if err := applyStorageLimit(&policy.NamespaceStopBytes, parsed, policy.Provenance.NamespaceStopBytes, PolicySourceEnvironment, true, "namespace_stop_bytes"); err != nil {
				return err
			}
			policy.Provenance.NamespaceStopBytes = PolicySourceEnvironment
		case "ORO_STORAGE_AGGREGATE_ADMISSION_BYTES":
			if err := applyStorageLimit(&policy.AggregateAdmissionBytes, parsed, policy.Provenance.AggregateAdmissionBytes, PolicySourceEnvironment, true, "aggregate_admission_bytes"); err != nil {
				return err
			}
			policy.Provenance.AggregateAdmissionBytes = PolicySourceEnvironment
		}
	}
	return nil
}

func applyCLIStoragePolicy(policy *StoragePolicy, overrides StoragePolicyOverrides) error {
	if overrides.NamespaceStopBytes == 0 {
		return nil
	}
	if err := applyStorageLimit(&policy.NamespaceStopBytes, overrides.NamespaceStopBytes, policy.Provenance.NamespaceStopBytes, PolicySourceCLI, true, "namespace_stop_bytes"); err != nil {
		return err
	}
	policy.Provenance.NamespaceStopBytes = PolicySourceCLI
	return nil
}
