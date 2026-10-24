package factory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// reserveManagedHostInvocation allocates core authority before backend effects.
// Separate reservations deliberately spend the token if scope insertion fails.
// Recovery never reuses the identity or refunds that invocation.
func reserveManagedHostInvocation(ctx context.Context, journal *managedJournal, generation uint64, deadline time.Time) (ScopeHandle, error) {
	if journal == nil || !time.Now().Before(deadline) || deadline.After(journal.admission.Deadline) {
		return ScopeHandle{}, errors.New("managed host allocation requires a live admitted work deadline")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ScopeHandle{}, fmt.Errorf("allocate managed host identity: %w", err)
	}
	a := journal.admission
	digest := sha256.Sum256([]byte(a.Reservation.StateRoot + "\x00" + a.ObjectiveID))
	scope := ScopeHandle{ID: hex.EncodeToString(random[:16]), Nonce: hex.EncodeToString(random[16:]), Generation: generation, Kind: "host", Backend: "launchd"}
	scope.Name = "dev.getoro.factory." + hex.EncodeToString(digest[:8]) + ".host." + scope.ID + "." + scope.Nonce
	if fresh, err := journal.reserveBudget(ctx, generation, "host", scope.ID, time.Now()); err != nil || !fresh {
		return ScopeHandle{}, errors.Join(errors.New("reserve fresh managed host budget"), err)
	}
	if fresh, err := journal.reserveScope(ctx, scope, deadline, time.Now()); err != nil || !fresh {
		return ScopeHandle{}, errors.Join(errors.New("reserve fresh managed host scope"), err)
	}
	return scope, nil
}

// One MiB covers escaped command metadata, scope identity and wrapper fields.
// The backend independently verifies its exact JSON envelope before writing it.
func validateManagedHostRequest(call HostInvocation, limits HostRetentionLimits) error {
	if int64(len(call.Stdin)) > limits.ArchiveBytes || call.OutputLimit <= 0 || call.OutputLimit > limits.HostOutputBytes {
		return errors.New("managed host request exceeds its admitted transport allowance")
	}
	remaining := int64(1<<20) - 4096
	for _, values := range [][]string{{call.Path, call.Dir}, call.Args, call.Env} {
		for _, value := range values {
			if strings.ContainsRune(value, 0) || int64(len(value)) > (remaining-3)/6 {
				return errors.New("managed host request exceeds its metadata allowance")
			}
			remaining -= int64(len(value))*6 + 3
		}
	}
	if 4*((int64(len(call.Stdin))+2)/3)+(1<<20) > limits.RequestBytes {
		return errors.New("managed host request exceeds its complete JSON allowance")
	}
	return nil
}
