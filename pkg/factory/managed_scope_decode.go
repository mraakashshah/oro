package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Scope bodies contain immutable identity only. Mutable lifecycle and peer
// fields must remain the separately scanned SQLite columns, never JSON inputs.
func decodeManagedScopeRecord(columns managedScopeRecord, body []byte) (managedScopeRecord, error) {
	var identity struct {
		ScopeHandle
		Deadline time.Time
	}
	if len(body) > 2048 {
		return managedScopeRecord{}, errors.New("managed scope identity exceeds its byte bound")
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		return managedScopeRecord{}, fmt.Errorf("decode managed scope identity: %w", err)
	}
	canonical, err := json.Marshal(identity)
	if err != nil || !bytes.Equal(canonical, body) {
		return managedScopeRecord{}, errors.New("managed scope identity is not its canonical encoding")
	}
	if err := validateManagedScope(identity.ScopeHandle); err != nil {
		return managedScopeRecord{}, err
	}
	if identity.Deadline.IsZero() || identity.Deadline.Year() < 1 || identity.Deadline.Location() != time.UTC {
		return managedScopeRecord{}, errors.New("managed scope requires its nonzero canonical UTC deadline")
	}
	if identity.ID != columns.ID || identity.Nonce != columns.Nonce || identity.Backend != columns.Backend || identity.Name != columns.Name {
		return managedScopeRecord{}, errors.New("managed scope columns differ from its exact identity")
	}
	if err := validateManagedScopeColumns(columns); err != nil {
		return managedScopeRecord{}, err
	}
	columns.ScopeHandle, columns.Deadline = identity.ScopeHandle, identity.Deadline
	return columns, nil
}

func validateManagedScopeColumns(record managedScopeRecord) error {
	// The generic journal permits distinct authenticated PID and PGID values.
	// Launchd enforces leader equality at its own kernel-authenticated boundary.
	if !validReservationPeer(record.PID, record.PGID) || len(record.CleanupEvidence) > 256 {
		return errors.New("managed scope has invalid peer or cleanup columns")
	}
	switch record.Phase {
	case "reserved":
		if record.PID != 0 || record.PGID != 0 {
			return errors.New("reserved managed scope already has an authenticated peer")
		}
	case "granted":
		if record.PID <= 1 || record.PGID <= 1 {
			return errors.New("granted managed scope lacks an authenticated peer")
		}
	case "revoked":
	case "quiescent":
		if record.CleanupEvidence == "" {
			return errors.New("quiescent managed scope lacks positive cleanup evidence")
		}
	default:
		return errors.New("managed scope has an unsupported lifecycle phase")
	}
	return nil
}
