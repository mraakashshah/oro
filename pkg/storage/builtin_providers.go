package storage

import (
	"os"
	"path/filepath"
)

// BuiltinProviders returns the cache providers supported without repository
// configuration. Missing executables remain valid because callers report them
// as skipped instead of treating an unavailable optional tool as invalid.
//
//oro:testonly
func BuiltinProviders() []CacheProvider {
	return []CacheProvider{
		{
			ID:              "go",
			Variables:       []string{"GOCACHE", "GOMODCACHE"},
			DefaultPath:     userCachePath("go-build"),
			Scope:           UserScope,
			Concurrency:     Concurrent,
			Ownership:       ToolNative,
			Status:          &OperationDescriptor{Executable: "go", Args: []string{"env", "GOCACHE"}},
			Cleaner:         CleanerDescriptor{Executable: "go", Args: []string{"clean", "-cache", "-modcache", "-fuzzcache"}, Trusted: true},
			ToolMayBeAbsent: true,
		},
		{
			ID:              "uv",
			Variables:       []string{"UV_CACHE_DIR"},
			DefaultPath:     userCachePath("uv"),
			Scope:           UserScope,
			Concurrency:     Concurrent,
			Ownership:       ToolNative,
			Status:          &OperationDescriptor{Executable: "uv", Args: []string{"cache", "dir"}},
			Cleaner:         CleanerDescriptor{Executable: "uv", Args: []string{"cache", "prune"}, Trusted: true},
			ToolMayBeAbsent: true,
		},
		{
			ID:              "golangci-lint",
			Variables:       []string{"GOLANGCI_LINT_CACHE"},
			DefaultPath:     userCachePath("golangci-lint"),
			Scope:           UserScope,
			Concurrency:     Serialized,
			Ownership:       ToolNative,
			Status:          &OperationDescriptor{Executable: "golangci-lint", Args: []string{"cache", "status"}},
			Cleaner:         CleanerDescriptor{Executable: "golangci-lint", Args: []string{"cache", "clean"}, Trusted: true},
			ToolMayBeAbsent: true,
		},
		{
			ID:              "npm",
			Variables:       []string{"NPM_CONFIG_CACHE"},
			DefaultPath:     userHomePath(".npm"),
			Scope:           UserScope,
			Concurrency:     Serialized,
			Ownership:       ToolNative,
			Status:          &OperationDescriptor{Executable: "npm", Args: []string{"cache", "verify"}},
			Cleaner:         CleanerDescriptor{Executable: "npm", Args: []string{"cache", "clean", "--force"}, Trusted: true},
			ToolMayBeAbsent: true,
		},
		{
			ID:              "npx",
			Variables:       []string{"NPM_CONFIG_CACHE"},
			DefaultPath:     userHomePath(".npm", "_npx"),
			Scope:           UserScope,
			Concurrency:     NoMaintenance,
			Ownership:       ToolNative,
			Status:          &OperationDescriptor{Executable: "npx", Args: []string{"--version"}},
			ToolMayBeAbsent: true,
		},
	}
}

func userCachePath(parts ...string) func() string {
	return func() string {
		root, err := os.UserCacheDir()
		if err != nil {
			root = filepath.Join(os.TempDir(), "cache")
		}
		return filepath.Join(append([]string{root}, parts...)...)
	}
}

func userHomePath(parts ...string) func() string {
	return func() string {
		root, err := os.UserHomeDir()
		if err != nil {
			root = os.TempDir()
		}
		return filepath.Join(append([]string{root}, parts...)...)
	}
}
