//go:build cgo && darwin

// Package memory — CGO linker search path for bundled native libraries.
// Gated to cgo && darwin because the only bundled libtokenizers.a archive is
// darwin-arm64. CI builds on Linux (both CGO_ENABLED=0 and CGO_ENABLED=1) must
// not attempt to link against a library we do not ship.
package memory

// #cgo darwin LDFLAGS: -L${SRCDIR}/lib
import "C"
