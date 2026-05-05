package beta

import "oro/pkg/codestruct/testdata/callgraph/alpha"

// Gamma calls alpha.Alpha, demonstrating cross-package in-project resolution.
//
//oro:testonly
func Gamma() {
	alpha.Alpha()
}
