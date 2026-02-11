// Package appversion provides build-time version information.
package appversion

// version is set at build time via -ldflags.
var version = "dev" //nolint:gochecknoglobals // ldflags requires package-level var

// String returns the current version.
func String() string {
	return version
}
