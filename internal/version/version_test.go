package version_test

import (
	"testing"

	"oro/internal/version"
)

func TestVersionIsSet(t *testing.T) {
	t.Parallel()

	v := version.String()
	if v == "" {
		t.Fatal("version.String() must not be empty")
	}
}
