// Package version provides build-time version information.
package version

// version is set at build time via -ldflags.
var version = "dev" //nolint:gochecknoglobals // ldflags requires package-level var

// String returns the current version.
func String() string {
	return version
}
