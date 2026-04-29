//go:build cgo && darwin

//nolint:testpackage // test needs the package's registered eval driver helper
package memoryeval

import (
	"testing"
)

func TestOpenEvalDBLoadsSqliteVec(t *testing.T) {
	db, err := OpenEvalDB(":memory:")
	if err != nil {
		t.Fatalf("OpenEvalDB(':memory:') error: %v", err)
	}
	defer db.Close()

	var version string
	if err := db.QueryRow("SELECT vec_version()").Scan(&version); err != nil {
		t.Fatalf("SELECT vec_version() error: %v", err)
	}
	if version == "" {
		t.Fatal("vec_version() returned empty string")
	}
	t.Logf("sqlite-vec version: %s", version)
}
