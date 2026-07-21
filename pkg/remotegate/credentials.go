// Package remotegate supplies the credential boundary for remote quality gates.
package remotegate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"oro/pkg/config"
)

// ErrCredentialInvalid indicates that a credential cannot be used for its configured role.
//
//oro:testonly
var ErrCredentialInvalid = errors.New("remote gate credential is invalid")

// Scope is a GitHub repository permission and access level pair.
//
//oro:testonly
type Scope string

const (
	// ScopeMetadataRead permits repository identity and policy reads.
	ScopeMetadataRead Scope = "Metadata-read"
	// ScopeContentsWrite permits authenticated Git ref operations.
	ScopeContentsWrite Scope = "Contents-write"
	// ScopePullRequestsWrite permits remote quality-gate pull request operations.
	ScopePullRequestsWrite Scope = "Pull-requests-write"
	// ScopeActionsWrite permits workflow dispatch and cancellation.
	ScopeActionsWrite Scope = "Actions-write"
	// ScopeChecksRead permits aggregate check observation.
	ScopeChecksRead Scope = "Checks-read"
	// ScopeWorkflowsWrite permits candidate commits that change workflow files.
	ScopeWorkflowsWrite Scope = "Workflows-write"
	// ScopeAdministrationWrite permits reconciliation of Oro-owned repository policy.
	ScopeAdministrationWrite Scope = "Administration-write"
)

// Credential is the redacted, short-lived result of a credential source.
//
//oro:testonly
type Credential struct {
	Token          string
	AppID          int64
	InstallationID int64
	Host           string
	Repository     string
	ExpiresAt      time.Time
	Scopes         []Scope
}

// CredentialTarget binds a credential source to one GitHub App installation and repository.
//
//oro:testonly
type CredentialTarget struct {
	Identity   config.GitHubAppIdentityConfig
	Host       string
	Repository string
}

// CredentialRequest identifies the exact credential that a source may resolve.
//
//oro:testonly
type CredentialRequest struct {
	Target CredentialTarget
	Scopes []Scope
}

// CredentialSource resolves a short-lived credential without persisting its secret.
//
//oro:testonly
type CredentialSource interface {
	Resolve(context.Context, CredentialRequest) (Credential, error)
}

// RuntimeCredentialProvider resolves credentials limited to runtime gate operations.
//
//oro:testonly
type RuntimeCredentialProvider interface {
	Resolve(context.Context) (Credential, error)
}

// MaintenanceCredentialProvider resolves credentials limited to policy maintenance.
//
//oro:testonly
type MaintenanceCredentialProvider interface {
	Resolve(context.Context) (Credential, error)
}

type credentialProvider struct {
	target CredentialTarget
	source CredentialSource
	scopes []Scope
}

// NewRuntimeCredentialProvider constructs a provider that rejects every permission outside the runtime allowlist.
//
//oro:testonly
func NewRuntimeCredentialProvider(target CredentialTarget, source CredentialSource) RuntimeCredentialProvider {
	return credentialProvider{target: target, source: source, scopes: runtimeScopes()}
}

// NewMaintenanceCredentialProvider constructs a provider that rejects every permission outside the maintenance allowlist.
//
//oro:testonly
func NewMaintenanceCredentialProvider(target CredentialTarget, source CredentialSource) MaintenanceCredentialProvider {
	return credentialProvider{target: target, source: source, scopes: maintenanceScopes()}
}

// Resolve returns a credential only when the resolved identity exactly matches the provider's role and target.
func (provider credentialProvider) Resolve(ctx context.Context) (Credential, error) {
	if err := validateTarget(provider.target); err != nil {
		return Credential{}, err
	}
	if provider.source == nil {
		return Credential{}, credentialInvalid("credential source is absent")
	}

	credential, err := provider.source.Resolve(ctx, CredentialRequest{
		Target: provider.target,
		Scopes: append([]Scope(nil), provider.scopes...),
	})
	if err != nil {
		return Credential{}, credentialInvalid("credential source could not resolve requested identity")
	}
	if err := validateCredential(credential, provider.target, provider.scopes); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func validateTarget(target CredentialTarget) error {
	identity := target.Identity
	if identity.Type != "github-app" || identity.AppID <= 0 || identity.InstallationID <= 0 || strings.TrimSpace(identity.PrivateKeyRef) == "" {
		return credentialInvalid("configured identity is invalid")
	}
	if strings.TrimSpace(target.Host) == "" || strings.TrimSpace(target.Repository) == "" {
		return credentialInvalid("configured host or repository is absent")
	}
	return nil
}

func validateCredential(credential Credential, target CredentialTarget, scopes []Scope) error {
	if strings.TrimSpace(credential.Token) == "" {
		return credentialInvalid("credential token is absent")
	}
	if !credential.ExpiresAt.After(time.Now()) {
		return credentialInvalid("credential is expired")
	}
	if credential.AppID != target.Identity.AppID || credential.InstallationID != target.Identity.InstallationID {
		return credentialInvalid("credential identity does not match configured identity")
	}
	if credential.Host != target.Host || credential.Repository != target.Repository {
		return credentialInvalid("credential target does not match configured target")
	}
	if !sameScopes(credential.Scopes, scopes) {
		return credentialInvalid("credential scopes do not match required allowlist")
	}
	return nil
}

func sameScopes(left, right []Scope) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]Scope(nil), left...)
	rightCopy := append([]Scope(nil), right...)
	slices.Sort(leftCopy)
	slices.Sort(rightCopy)
	return slices.Equal(leftCopy, rightCopy)
}

func runtimeScopes() []Scope {
	return []Scope{
		ScopeMetadataRead,
		ScopeContentsWrite,
		ScopePullRequestsWrite,
		ScopeActionsWrite,
		ScopeChecksRead,
		ScopeWorkflowsWrite,
	}
}

func maintenanceScopes() []Scope {
	return []Scope{
		ScopeMetadataRead,
		ScopeAdministrationWrite,
	}
}

func credentialInvalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrCredentialInvalid, reason)
}
