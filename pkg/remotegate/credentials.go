package remotegate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"oro/pkg/config"
)

// ErrCredentialInvalid indicates a credential that does not exactly satisfy
// the immutable identity, repository, role, or permission contract.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
var ErrCredentialInvalid = errors.New("invalid remote gate credential")

// CredentialRole identifies the least-privilege operation a credential may
// authorize.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type CredentialRole string

// Credential roles separate immutable runtime operations from policy maintenance.
const (
	// CredentialRoleRuntime authorizes immutable remote-gate execution.
	CredentialRoleRuntime CredentialRole = "runtime"
	// CredentialRoleMaintenance authorizes managed policy reconciliation.
	CredentialRoleMaintenance CredentialRole = "maintenance"
)

// CredentialTarget binds a GitHub App identity to exactly one repository.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type CredentialTarget struct {
	Identity config.GitHubAppIdentityConfig
	Host     string
	Owner    string
	Name     string
}

// CredentialRequest asks a source for one exact role and permission set.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type CredentialRequest struct {
	Identity    config.GitHubAppIdentityConfig
	Host        string
	Owner       string
	Name        string
	Role        CredentialRole
	Permissions map[string]string
}

// Credential is the validated, repository-bound credential returned by a
// CredentialSource.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type Credential struct {
	Token          string
	Role           CredentialRole
	AppID          int64
	InstallationID int64
	Host           string
	Owner          string
	Name           string
	Permissions    map[string]string
	ExpiresAt      time.Time
}

// CredentialSource resolves a credential without exposing its backing store.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type CredentialSource interface {
	Resolve(context.Context, CredentialRequest) (Credential, error)
}

// RuntimeCredentialProvider resolves only immutable runtime credentials.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type RuntimeCredentialProvider struct {
	target   CredentialTarget
	source   CredentialSource
	identity *runtimeCredentialProviderIdentity
}

type runtimeCredentialProviderIdentity struct{ allocated byte }

// MaintenanceCredentialProvider resolves only policy-maintenance credentials.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
type MaintenanceCredentialProvider struct {
	target CredentialTarget
	source CredentialSource
}

// NewRuntimeCredentialProvider constructs a provider with fixed runtime
// permissions owned by the provider rather than its callers.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
func NewRuntimeCredentialProvider(target CredentialTarget, source CredentialSource) RuntimeCredentialProvider {
	return RuntimeCredentialProvider{target: target, source: source, identity: &runtimeCredentialProviderIdentity{allocated: 1}}
}

// NewMaintenanceCredentialProvider constructs a provider with fixed
// maintenance permissions owned by the provider rather than its callers.
//
//oro:testonly — credential providers are wired by subsequent remote-gate tasks.
func NewMaintenanceCredentialProvider(target CredentialTarget, source CredentialSource) MaintenanceCredentialProvider {
	return MaintenanceCredentialProvider{target: target, source: source}
}

// Resolve returns the exact runtime credential or a token-safe invalid error.
func (provider RuntimeCredentialProvider) Resolve(ctx context.Context) (Credential, error) {
	return resolveCredential(ctx, provider.target, provider.source, CredentialRoleRuntime, runtimeCredentialPermissions())
}

// MatchesRepository reports whether this non-zero runtime provider is bound
// to exactly the supplied repository.
func (provider RuntimeCredentialProvider) MatchesRepository(repository Repository) bool {
	return provider.identity != nil && provider.source != nil && validCredentialTarget(provider.target) &&
		provider.target.Host == repository.Host && provider.target.Owner == repository.Owner && provider.target.Name == repository.Name
}

// SameActorScope reports whether two components carry the same provider
// instance. Copies of one provider retain identity; separately constructed
// providers do not, even when their public repository coordinates match.
func (provider RuntimeCredentialProvider) SameActorScope(other RuntimeCredentialProvider) bool {
	return provider.identity != nil && provider.identity == other.identity
}

// Resolve returns the exact maintenance credential or a token-safe invalid error.
func (provider MaintenanceCredentialProvider) Resolve(ctx context.Context) (Credential, error) {
	return resolveCredential(ctx, provider.target, provider.source, CredentialRoleMaintenance, maintenanceCredentialPermissions())
}

func resolveCredential(ctx context.Context, target CredentialTarget, source CredentialSource, role CredentialRole, permissions map[string]string) (Credential, error) {
	if source == nil {
		return Credential{}, invalidCredential("credential source is required")
	}
	if !validCredentialTarget(target) {
		return Credential{}, invalidCredential("credential target is invalid")
	}
	credential, err := source.Resolve(ctx, CredentialRequest{
		Identity:    target.Identity,
		Host:        target.Host,
		Owner:       target.Owner,
		Name:        target.Name,
		Role:        role,
		Permissions: clonePermissions(permissions),
	})
	if err != nil {
		if errors.Is(err, ErrTransient) {
			return Credential{}, fmt.Errorf("%w: credential source unavailable", ErrTransient)
		}
		return Credential{}, invalidCredential("credential source rejected request")
	}
	if !validCredential(credential, target, role, permissions, time.Now()) {
		return Credential{}, invalidCredential("credential does not match request")
	}
	return credential, nil
}

func validCredentialTarget(target CredentialTarget) bool {
	identity := target.Identity
	return identity.Type == "github-app" && identity.AppID > 0 && identity.InstallationID > 0 && validPrivateKeyRef(identity.PrivateKeyRef) && nonEmpty(target.Host) && nonEmpty(target.Owner) && nonEmpty(target.Name)
}

func validCredential(credential Credential, target CredentialTarget, role CredentialRole, permissions map[string]string, now time.Time) bool {
	return nonEmpty(credential.Token) && credential.Role == role && credential.AppID == target.Identity.AppID && credential.InstallationID == target.Identity.InstallationID && credential.Host == target.Host && credential.Owner == target.Owner && credential.Name == target.Name && credential.ExpiresAt.After(now) && exactPermissions(credential.Permissions, permissions)
}

func runtimeCredentialPermissions() map[string]string {
	return map[string]string{
		"metadata":      "read",
		"contents":      "write",
		"pull_requests": "write",
		"actions":       "write",
		"checks":        "read",
		"workflows":     "write",
	}
}

func maintenanceCredentialPermissions() map[string]string {
	return map[string]string{
		"metadata":       "read",
		"administration": "write",
	}
}

func exactPermissions(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for permission, level := range want {
		if got[permission] != level {
			return false
		}
	}
	return true
}

func clonePermissions(permissions map[string]string) map[string]string {
	clone := make(map[string]string, len(permissions))
	for permission, level := range permissions {
		clone[permission] = level
	}
	return clone
}

func validPrivateKeyRef(ref string) bool {
	const scheme = "keychain:"
	if !strings.HasPrefix(ref, scheme) || len(ref) == len(scheme) {
		return false
	}
	for _, char := range ref[len(scheme):] {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func nonEmpty(value string) bool {
	return strings.TrimSpace(value) != ""
}

func invalidCredential(reason string) error {
	return fmt.Errorf("%w: %s", ErrCredentialInvalid, reason)
}
