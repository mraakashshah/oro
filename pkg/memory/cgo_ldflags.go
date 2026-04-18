// Package memory — CGO linker search path for bundled native libraries.
package memory

// #cgo darwin LDFLAGS: -L${SRCDIR}/lib
// #cgo linux LDFLAGS: -L${SRCDIR}/lib
import "C"
