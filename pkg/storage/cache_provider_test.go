package storage_test

import (
	"errors"
	"testing"

	"oro/pkg/storage"
)

func TestCacheProviderValidation(t *testing.T) {
	t.Parallel()

	defaultPath := func() string { return "/cache" }
	valid := func(scope storage.CacheScope) storage.CacheProvider {
		return storage.CacheProvider{
			ID:              "go-build",
			Variables:       []string{"GOCACHE"},
			DefaultPath:     defaultPath,
			Scope:           scope,
			Concurrency:     storage.Concurrent,
			Ownership:       storage.ToolNative,
			Cleaner:         storage.CleanerDescriptor{Executable: "go", Args: []string{"clean", "-cache"}, Trusted: true},
			ToolMayBeAbsent: true,
		}
	}

	for _, scope := range []storage.CacheScope{storage.UserScope, storage.ProjectScope, storage.RepositoryScope} {
		t.Run(string(scope), func(t *testing.T) {
			t.Parallel()
			if err := valid(scope).Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	t.Run("empty ID", func(t *testing.T) {
		provider := valid(storage.UserScope)
		provider.ID = ""
		if err := provider.Validate(); !errors.Is(err, storage.ErrInvalidProvider) {
			t.Fatalf("Validate() error = %v, want ErrInvalidProvider", err)
		}
	})

	t.Run("duplicate cache variable", func(t *testing.T) {
		provider := valid(storage.ProjectScope)
		provider.Variables = []string{"GOCACHE", "GOCACHE"}
		if err := provider.Validate(); !errors.Is(err, storage.ErrDuplicateCacheVar) {
			t.Fatalf("Validate() error = %v, want ErrDuplicateCacheVar", err)
		}
	})

	t.Run("untrusted cleaner", func(t *testing.T) {
		provider := valid(storage.RepositoryScope)
		provider.Cleaner.Trusted = false
		if err := provider.Validate(); !errors.Is(err, storage.ErrUntrustedCleaner) {
			t.Fatalf("Validate() error = %v, want ErrUntrustedCleaner", err)
		}
	})
}
