package factory //nolint:testpackage // Exercise the private tree encoder topology.

import (
	"strings"
	"testing"
)

func TestSourceGitDirectoryTopology(t *testing.T) {
	tests := []struct {
		name   string
		dir    *sourceGitDirectory
		result *sourceTree
		want   string
	}{
		{name: "nil root", result: &sourceTree{}, want: "nil directory"},
		{name: "nil child", dir: &sourceGitDirectory{children: map[string]*sourceGitDirectory{"invalid": nil}}, result: &sourceTree{}, want: "nil directory"},
		{name: "nil nested child", dir: &sourceGitDirectory{children: map[string]*sourceGitDirectory{"parent": {children: map[string]*sourceGitDirectory{"invalid": nil}}}}, result: &sourceTree{}, want: "nil directory"},
		{name: "nil result", dir: &sourceGitDirectory{}, want: "result is nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid, err := tt.dir.encode(40, tt.result)
			if err == nil || !strings.Contains(err.Error(), tt.want) || oid != "" {
				t.Fatalf("encode() = %q, %v; want empty ID and error containing %q", oid, err, tt.want)
			}
		})
	}
}
