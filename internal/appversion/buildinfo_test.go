package appversion_test

import (
	"testing"

	"oro/internal/appversion"
)

func TestVersionIsSet(t *testing.T) {
	t.Parallel()

	v := appversion.String()
	if v == "" {
		t.Fatal("version.String() must not be empty")
	}
}
