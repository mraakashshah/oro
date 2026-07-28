package remotegate_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
	"oro/pkg/remotegate"
)

func TestCredentialScopesAreNoninterchangeable(t *testing.T) {
	target := remotegate.CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{
			Type:           "github-app",
			AppID:          42,
			InstallationID: 99,
			PrivateKeyRef:  "keychain:oro/github-app",
		},
		Host:  "github.example",
		Owner: "oro",
		Name:  "oro",
	}
	runtimeSource := &credentialSource{credential: validCredential(target, remotegate.CredentialRoleRuntime, map[string]string{
		"metadata":      "read",
		"contents":      "write",
		"pull_requests": "write",
		"actions":       "write",
		"checks":        "read",
		"workflows":     "write",
	})}
	maintenanceSource := &credentialSource{credential: validCredential(target, remotegate.CredentialRoleMaintenance, map[string]string{
		"metadata":       "read",
		"administration": "write",
	})}

	runtime := remotegate.NewRuntimeCredentialProvider(target, runtimeSource)
	maintenance := remotegate.NewMaintenanceCredentialProvider(target, maintenanceSource)

	if _, err := runtime.Resolve(context.Background()); err != nil {
		t.Fatalf("runtime Resolve() error = %v", err)
	}
	if _, err := maintenance.Resolve(context.Background()); err != nil {
		t.Fatalf("maintenance Resolve() error = %v", err)
	}
	if got, want := runtimeSource.request.Permissions, runtimePermissions(); !reflect.DeepEqual(got, want) {
		t.Errorf("runtime request permissions = %#v, want %#v", got, want)
	}
	if got, want := maintenanceSource.request.Permissions, maintenancePermissions(); !reflect.DeepEqual(got, want) {
		t.Errorf("maintenance request permissions = %#v, want %#v", got, want)
	}

	for name, resolve := range map[string]func() error{
		"runtime rejects maintenance credential": func() error {
			_, err := remotegate.NewRuntimeCredentialProvider(target, maintenanceSource).Resolve(context.Background())
			return err
		},
		"maintenance rejects runtime credential": func() error {
			_, err := remotegate.NewMaintenanceCredentialProvider(target, runtimeSource).Resolve(context.Background())
			return err
		},
	} {
		if err := resolve(); !errors.Is(err, remotegate.ErrCredentialInvalid) {
			t.Errorf("%s error = %v, want wrapped ErrCredentialInvalid", name, err)
		}
	}

	for name, mutate := range map[string]func(*remotegate.Credential){
		"missing token":          func(credential *remotegate.Credential) { credential.Token = "" },
		"expired token":          func(credential *remotegate.Credential) { credential.ExpiresAt = time.Now().Add(-time.Minute) },
		"wrong app":              func(credential *remotegate.Credential) { credential.AppID++ },
		"wrong installation":     func(credential *remotegate.Credential) { credential.InstallationID++ },
		"wrong host":             func(credential *remotegate.Credential) { credential.Host = "other.example" },
		"wrong owner":            func(credential *remotegate.Credential) { credential.Owner = "other" },
		"wrong repository":       func(credential *remotegate.Credential) { credential.Name = "other" },
		"missing permission":     func(credential *remotegate.Credential) { delete(credential.Permissions, "actions") },
		"extra permission":       func(credential *remotegate.Credential) { credential.Permissions["issues"] = "write" },
		"wrong permission value": func(credential *remotegate.Credential) { credential.Permissions["contents"] = "read" },
	} {
		t.Run(name, func(t *testing.T) {
			credential := validCredential(target, remotegate.CredentialRoleRuntime, runtimePermissions())
			mutate(&credential)
			source := &credentialSource{credential: credential}
			_, err := remotegate.NewRuntimeCredentialProvider(target, source).Resolve(context.Background())
			assertCredentialInvalidWithoutToken(t, err, credential.Token)
		})
	}

	for name, provider := range map[string]remotegate.CredentialSource{
		"nil source":   nil,
		"source error": &credentialSource{err: errors.New("secret-token source error")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := remotegate.NewRuntimeCredentialProvider(target, provider).Resolve(context.Background())
			assertCredentialInvalidWithoutToken(t, err, "secret-token")
		})
	}

	malformedTarget := target
	malformedTarget.Identity.PrivateKeyRef = "not-a-keychain-reference"
	if _, err := remotegate.NewRuntimeCredentialProvider(malformedTarget, runtimeSource).Resolve(context.Background()); !errors.Is(err, remotegate.ErrCredentialInvalid) {
		t.Errorf("malformed identity error = %v, want wrapped ErrCredentialInvalid", err)
	}
}

type credentialSource struct {
	credential remotegate.Credential
	err        error
	request    remotegate.CredentialRequest
}

func (source *credentialSource) Resolve(_ context.Context, request remotegate.CredentialRequest) (remotegate.Credential, error) {
	source.request = request
	return source.credential, source.err
}

func validCredential(target remotegate.CredentialTarget, role remotegate.CredentialRole, permissions map[string]string) remotegate.Credential {
	return remotegate.Credential{
		Token:          "secret-token",
		Role:           role,
		AppID:          target.Identity.AppID,
		InstallationID: target.Identity.InstallationID,
		Host:           target.Host,
		Owner:          target.Owner,
		Name:           target.Name,
		Permissions:    permissions,
		ExpiresAt:      time.Now().Add(time.Hour),
	}
}

func runtimePermissions() map[string]string {
	return map[string]string{
		"metadata":      "read",
		"contents":      "write",
		"pull_requests": "write",
		"actions":       "write",
		"checks":        "read",
		"workflows":     "write",
	}
}

func maintenancePermissions() map[string]string {
	return map[string]string{
		"metadata":       "read",
		"administration": "write",
	}
}

func assertCredentialInvalidWithoutToken(t *testing.T, err error, token string) {
	t.Helper()
	if !errors.Is(err, remotegate.ErrCredentialInvalid) {
		t.Fatalf("error = %v, want wrapped ErrCredentialInvalid", err)
	}
	if token != "" && strings.Contains(err.Error(), token) {
		t.Errorf("error leaked token: %q", err)
	}
}
