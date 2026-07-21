// Package storage defines repository-agnostic storage policy contracts.
package storage

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidProvider reports a malformed cache provider descriptor.
	ErrInvalidProvider = errors.New("invalid cache provider")
	// ErrDuplicateCacheVar reports a provider which declares the same variable twice.
	ErrDuplicateCacheVar = errors.New("duplicate cache variable")
	// ErrUntrustedCleaner reports a cleaner not authorized by trusted configuration.
	ErrUntrustedCleaner = errors.New("untrusted cache cleaner")
)

// CacheScope determines which callers may share a cache root.
type CacheScope string

const (
	// UserScope shares a cache across every project belonging to one user.
	UserScope CacheScope = "user"
	// ProjectScope shares a cache across worktrees for one project.
	ProjectScope CacheScope = "project"
	// RepositoryScope shares a cache across worktrees for one repository.
	RepositoryScope CacheScope = "repository"
)

// ConcurrencyMode determines how Oro coordinates a provider's operations.
type ConcurrencyMode string

const (
	// Concurrent permits simultaneous provider operations.
	Concurrent ConcurrencyMode = "concurrent"
	// Serialized permits only one provider operation at a time.
	Serialized ConcurrencyMode = "serialized"
	// NoMaintenance disables automated provider maintenance.
	NoMaintenance ConcurrencyMode = "no_automated_maintenance"
)

// CacheOwnership identifies who owns the provider's cache root.
type CacheOwnership string

const (
	// ToolNative identifies a root managed by the cache's tool.
	ToolNative CacheOwnership = "tool_native"
	// OroManaged identifies a root maintained by Oro.
	OroManaged CacheOwnership = "oro_managed"
)

// OperationDescriptor identifies a fixed tool operation without shell parsing.
type OperationDescriptor struct {
	Executable string
	Args       []string
}

// CleanerDescriptor identifies an optional cache cleanup operation.
//
// Trusted is set only after trusted user-level policy authorizes the operation.
type CleanerDescriptor struct {
	Executable string
	Args       []string
	Trusted    bool
}

// CacheProvider describes one scoped external cache provider.
type CacheProvider struct {
	ID              string
	Variables       []string
	DefaultPath     func() string
	Scope           CacheScope
	Concurrency     ConcurrencyMode
	Ownership       CacheOwnership
	Status          *OperationDescriptor
	Cleaner         CleanerDescriptor
	ToolMayBeAbsent bool
}

// Validate confirms that p is safe to register in a shared cache policy.
func (p CacheProvider) Validate() error {
	if strings.TrimSpace(p.ID) == "" || p.DefaultPath == nil || !p.Scope.valid() ||
		!p.Concurrency.valid() || !p.Ownership.valid() {
		return ErrInvalidProvider
	}
	if err := validateVariables(p.Variables); err != nil {
		return err
	}
	if err := validateOperation(p.Status); err != nil {
		return err
	}
	if !p.Cleaner.present() {
		return nil
	}
	if !p.Cleaner.Trusted {
		return ErrUntrustedCleaner
	}
	return validateOperation(&OperationDescriptor{
		Executable: p.Cleaner.Executable,
		Args:       p.Cleaner.Args,
	})
}

func (s CacheScope) valid() bool {
	return s == UserScope || s == ProjectScope || s == RepositoryScope
}

func (m ConcurrencyMode) valid() bool {
	return m == Concurrent || m == Serialized || m == NoMaintenance
}

func (o CacheOwnership) valid() bool {
	return o == ToolNative || o == OroManaged
}

func validateVariables(variables []string) error {
	seen := make(map[string]struct{}, len(variables))
	for _, variable := range variables {
		if strings.TrimSpace(variable) == "" {
			return ErrInvalidProvider
		}
		if _, exists := seen[variable]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateCacheVar, variable)
		}
		seen[variable] = struct{}{}
	}
	return nil
}

func validateOperation(operation *OperationDescriptor) error {
	if operation == nil {
		return nil
	}
	if strings.TrimSpace(operation.Executable) == "" {
		return ErrInvalidProvider
	}
	return nil
}

func (c CleanerDescriptor) present() bool {
	return c.Executable != "" || len(c.Args) > 0 || c.Trusted
}
