package factory

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

var dockerCreatedAtShape = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

// CanonicalDockerCreatedAt converts a daemon timestamp to its canonical UTC
// identity. Input is strict RFC3339 with at most nine fractional digits and
// 35 bytes, so normalization never silently truncates timestamp precision.
// Persisted ownership identities must still pass their canonical validators.
func CanonicalDockerCreatedAt(value string) (string, error) {
	if len(value) > 35 || !dockerCreatedAtShape.MatchString(value) {
		return "", errors.New("invalid Docker creation timestamp syntax or length")
	}
	created, err := time.ParseInLocation(time.RFC3339Nano, value, time.UTC)
	if err != nil {
		return "", fmt.Errorf("parse Docker creation timestamp: %w", err)
	}
	created = created.UTC()
	if created.IsZero() || created.Year() < 1 || created.Year() > 9999 {
		return "", errors.New("invalid Docker creation timestamp: zero or outside the UTC year range")
	}
	return created.Format(time.RFC3339Nano), nil
}
