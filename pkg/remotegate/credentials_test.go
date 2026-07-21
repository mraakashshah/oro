package remotegate //nolint:testpackage // acceptance test verifies the complete credential boundary.

import (
	"context"
	"errors"
	"testing"
	"time"

	"oro/pkg/config"
)

func TestCredentialScopesAreNoninterchangeable(t *testing.T) {
	t.Parallel()

	target := CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{
			Type:           "github-app",
			AppID:          123,
			InstallationID: 456,
			PrivateKeyRef:  "keychain:oro/test",
		},
		Host:       "github.example.com",
		Repository: "acme/oro",
	}

	t.Run("runtime accepts only the runtime scope", func(t *testing.T) {
		t.Parallel()
		credential, err := NewRuntimeCredentialProvider(target, staticCredentialSource{credential: credentialFor(target, runtimeScopes())}).Resolve(context.Background())
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		assertExactScopes(t, credential.Scopes, runtimeScopes())
	})

	t.Run("maintenance accepts only the maintenance scope", func(t *testing.T) {
		t.Parallel()
		credential, err := NewMaintenanceCredentialProvider(target, staticCredentialSource{credential: credentialFor(target, maintenanceScopes())}).Resolve(context.Background())
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		assertExactScopes(t, credential.Scopes, maintenanceScopes())
	})

	for _, tt := range []struct {
		name    string
		resolve func(CredentialSource) (Credential, error)
		mutate  func(*Credential)
	}{
		{
			name: "runtime rejects maintenance credential",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.Scopes = maintenanceScopes()
			},
		},
		{
			name: "maintenance rejects runtime credential",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewMaintenanceCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.Scopes = runtimeScopes()
			},
		},
		{
			name: "rejects absent token",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.Token = ""
			},
		},
		{
			name: "rejects expired credential",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.ExpiresAt = time.Now().Add(-time.Minute)
			},
		},
		{
			name: "rejects overprivileged credential",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.Scopes = append(credential.Scopes, ScopeAdministrationWrite)
			},
		},
		{
			name: "rejects wrong host",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.Host = "github.invalid"
			},
		},
		{
			name: "rejects wrong repository",
			resolve: func(source CredentialSource) (Credential, error) {
				return NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			},
			mutate: func(credential *Credential) {
				credential.Repository = "acme/other"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			credential := credentialFor(target, runtimeScopes())
			tt.mutate(&credential)
			_, err := tt.resolve(staticCredentialSource{credential: credential})
			if !errors.Is(err, ErrCredentialInvalid) {
				t.Fatalf("Resolve() error = %v, want ErrCredentialInvalid", err)
			}
		})
	}
}

type staticCredentialSource struct {
	credential Credential
	err        error
}

func (source staticCredentialSource) Resolve(context.Context, CredentialRequest) (Credential, error) {
	return source.credential, source.err
}

func credentialFor(target CredentialTarget, scopes []Scope) Credential {
	return Credential{
		Token:          "secret-token",
		AppID:          target.Identity.AppID,
		InstallationID: target.Identity.InstallationID,
		Host:           target.Host,
		Repository:     target.Repository,
		ExpiresAt:      time.Now().Add(time.Hour),
		Scopes:         append([]Scope(nil), scopes...),
	}
}

func assertExactScopes(t *testing.T, got, want []Scope) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("scope count = %d, want %d (%v)", len(got), len(want), want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("scope[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
