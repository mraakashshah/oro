package dispatcher

import (
	"testing"

	"github.com/google/uuid"
)

func TestGenerateCheckpointIDReturnsUUIDv7(t *testing.T) {
	id := generateCheckpointID()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("generateCheckpointID() = %q, want RFC 9562 UUID: %v", id, err)
	}
	if got := parsed.Version(); got != uuid.Version(7) {
		t.Fatalf("generateCheckpointID() version = %d, want 7", got)
	}
}
