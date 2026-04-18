//go:build !integration && cgo && darwin

package memory

import "fmt"

// newORTSession is a stub for non-integration builds.
// Real ORT sessions are created in bge_ort_real.go under the integration build tag.
func newORTSession(_ string) (ortSession, error) {
	return nil, fmt.Errorf("ORT sessions require the integration build tag")
}
