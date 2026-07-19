package main

import (
	"context"
	"os"
	"testing"
)

func TestOpenStorageCatalogUsesGlobalCatalogPath(t *testing.T) {
	t.Parallel()

	oroHome := t.TempDir()
	catalog, err := openStorageCatalog(context.Background(), oroHome)
	if err != nil {
		t.Fatalf("openStorageCatalog() error = %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		t.Fatalf("ResolveStoragePaths() error = %v", err)
	}
	if _, err := os.Stat(paths.CatalogPath); err != nil {
		t.Fatalf("catalog path %q was not created: %v", paths.CatalogPath, err)
	}
}
