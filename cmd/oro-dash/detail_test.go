package main

import (
	"os"
	"strings"
	"testing"
)

func TestLegacyDetailModelRemoved(t *testing.T) {
	source, err := os.ReadFile("detail.go")
	if err != nil {
		t.Fatalf("read legacy detail source: %v", err)
	}
	for _, legacy := range []string{"DefaultTheme", "NewStyles", "newDetailModel", "renderOverviewTab"} {
		if strings.Contains(string(source), legacy) {
			t.Fatalf("legacy detail symbol %q should be removed from oro-dash", legacy)
		}
	}
}
