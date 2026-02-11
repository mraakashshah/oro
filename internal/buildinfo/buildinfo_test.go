package buildinfo_test

import (
	"testing"

	"oro/internal/buildinfo"
)

func TestVersionIsSet(t *testing.T) {
	t.Parallel()

	v := buildinfo.String()
	if v == "" {
		t.Fatal("version.String() must not be empty")
	}
}
