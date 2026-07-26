package storage_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"oro/pkg/storage"
)

func TestBuiltinProviders(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir() error = %v", err)
	}

	tests := []struct {
		id          string
		variables   []string
		defaultPath string
		scope       storage.CacheScope
		mode        storage.ConcurrencyMode
		ownership   storage.CacheOwnership
		status      *storage.OperationDescriptor
		cleaner     storage.CleanerDescriptor
	}{
		{
			id:          "go",
			variables:   []string{"GOCACHE", "GOMODCACHE"},
			defaultPath: filepath.Join(cacheRoot, "go-build"),
			scope:       storage.UserScope,
			mode:        storage.Concurrent,
			ownership:   storage.ToolNative,
			status:      &storage.OperationDescriptor{Executable: "go", Args: []string{"env", "GOCACHE"}},
			cleaner:     storage.CleanerDescriptor{Executable: "go", Args: []string{"clean", "-cache", "-modcache", "-fuzzcache"}, Trusted: true},
		},
		{
			id:          "uv",
			variables:   []string{"UV_CACHE_DIR"},
			defaultPath: filepath.Join(cacheRoot, "uv"),
			scope:       storage.UserScope,
			mode:        storage.Concurrent,
			ownership:   storage.ToolNative,
			status:      &storage.OperationDescriptor{Executable: "uv", Args: []string{"cache", "dir"}},
			cleaner:     storage.CleanerDescriptor{Executable: "uv", Args: []string{"cache", "prune"}, Trusted: true},
		},
		{
			id:          "golangci-lint",
			variables:   []string{"GOLANGCI_LINT_CACHE"},
			defaultPath: filepath.Join(cacheRoot, "golangci-lint"),
			scope:       storage.UserScope,
			mode:        storage.Serialized,
			ownership:   storage.ToolNative,
			status:      &storage.OperationDescriptor{Executable: "golangci-lint", Args: []string{"cache", "status"}},
			cleaner:     storage.CleanerDescriptor{Executable: "golangci-lint", Args: []string{"cache", "clean"}, Trusted: true},
		},
		{
			id:          "npm",
			variables:   []string{"NPM_CONFIG_CACHE"},
			defaultPath: filepath.Join(homeDir, ".npm"),
			scope:       storage.UserScope,
			mode:        storage.Serialized,
			ownership:   storage.ToolNative,
			status:      &storage.OperationDescriptor{Executable: "npm", Args: []string{"cache", "verify"}},
			cleaner:     storage.CleanerDescriptor{Executable: "npm", Args: []string{"cache", "clean", "--force"}, Trusted: true},
		},
		{
			id:          "npx",
			variables:   []string{"NPM_CONFIG_CACHE"},
			defaultPath: filepath.Join(homeDir, ".npm", "_npx"),
			scope:       storage.UserScope,
			mode:        storage.NoMaintenance,
			ownership:   storage.ToolNative,
			status:      &storage.OperationDescriptor{Executable: "npx", Args: []string{"--version"}},
		},
	}

	builtins := storage.BuiltinProviders()
	if len(builtins) != len(tests) {
		t.Fatalf("BuiltinProviders() returned %d providers, want %d", len(builtins), len(tests))
	}
	providers := providerByID(builtins)
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			provider, ok := providers[test.id]
			if !ok {
				t.Fatalf("BuiltinProviders() missing %q", test.id)
			}
			assertProvider(t, provider, test.variables, test.defaultPath, test.scope, test.mode, test.ownership, test.status, test.cleaner)
		})
	}
}

func TestNPMProviderMaintenanceDescriptor(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	provider, ok := providerByID(storage.BuiltinProviders())["npm"]
	if !ok {
		t.Fatal("BuiltinProviders() missing npm provider")
	}

	if got, want := provider.Cleaner, (storage.CleanerDescriptor{
		Executable: "npm",
		Args:       []string{"cache", "clean", "--force"},
		Trusted:    true,
	}); !reflect.DeepEqual(got, want) {
		t.Errorf("Cleaner = %#v, want fixed npm argv %#v", got, want)
	}
	if !provider.ToolMayBeAbsent {
		t.Error("ToolMayBeAbsent = false, want true so an unavailable npm is reported as skipped")
	}
	if got, want := provider.Variables, []string{"NPM_CONFIG_CACHE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Variables = %q, want %q to retain the shared npm cache identity", got, want)
	}
	if got, want := provider.DefaultPath(), filepath.Join(homeDir, ".npm"); got != want {
		t.Errorf("DefaultPath() = %q, want npm cache root %q", got, want)
	}
	for _, adjacent := range []string{filepath.Join(homeDir, ".npm", "_npx"), filepath.Join(homeDir, "node_modules", ".cache")} {
		if got := provider.DefaultPath(); got == adjacent {
			t.Errorf("DefaultPath() = %q, must not select npm-adjacent directory %q", got, adjacent)
		}
	}
}

func providerByID(providers []storage.CacheProvider) map[string]storage.CacheProvider {
	byID := make(map[string]storage.CacheProvider, len(providers))
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	return byID
}

func assertProvider(
	t *testing.T,
	provider storage.CacheProvider,
	variables []string,
	defaultPath string,
	scope storage.CacheScope,
	mode storage.ConcurrencyMode,
	ownership storage.CacheOwnership,
	status *storage.OperationDescriptor,
	cleaner storage.CleanerDescriptor,
) {
	t.Helper()
	if !reflect.DeepEqual(provider.Variables, variables) {
		t.Errorf("Variables = %q, want %q", provider.Variables, variables)
	}
	if path := provider.DefaultPath(); path != defaultPath {
		t.Errorf("DefaultPath() = %q, want %q", path, defaultPath)
	}
	if provider.Scope != scope {
		t.Errorf("Scope = %q, want %q", provider.Scope, scope)
	}
	if provider.Concurrency != mode {
		t.Errorf("Concurrency = %q, want %q", provider.Concurrency, mode)
	}
	if provider.Ownership != ownership {
		t.Errorf("Ownership = %q, want %q", provider.Ownership, ownership)
	}
	if !reflect.DeepEqual(provider.Status, status) {
		t.Errorf("Status = %#v, want %#v", provider.Status, status)
	}
	if !reflect.DeepEqual(provider.Cleaner, cleaner) {
		t.Errorf("Cleaner = %#v, want %#v", provider.Cleaner, cleaner)
	}
	if !provider.ToolMayBeAbsent {
		t.Error("ToolMayBeAbsent = false, want true")
	}
	if err := provider.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}
