package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ReleasableHostReservation returns immutable capacity identity only after
// monotonic supervision stop and positive retirement of every execution scope.
// A false result or error preserves the reservation. The caller must compare the
// complete returned tuple to its ledger and durably release it at most once.
// Eligibility neither changes nor asserts the objective's delivery outcome.
func ReleasableHostReservation(ctx context.Context, stateDir, objectiveID string) (HostReservation, bool, error) {
	if err := ctx.Err(); err != nil {
		return HostReservation{}, false, fmt.Errorf("host reservation query canceled: %w", err)
	}
	journal, err := openManagedJournal(ctx, stateDir, objectiveID)
	if err != nil {
		return HostReservation{}, false, err
	}
	defer func() { _ = journal.Close() }()
	var eligible bool
	err = journal.mutate(ctx, "", "", func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if state.DesiredState != "stopped" {
			return false, nil
		}
		var queryErr error
		eligible, queryErr = reservationScopesQuiescent(ctx, tx, journal.admission, state.Generation)
		return false, queryErr // Read-only ownership transaction: never advance revision.
	})
	if err != nil {
		return HostReservation{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return HostReservation{}, false, fmt.Errorf("host reservation query canceled: %w", err)
	}
	if !eligible {
		return HostReservation{}, false, nil
	}
	return journal.admission.Reservation, true, nil
}

func reservationScopesQuiescent(ctx context.Context, tx *sql.Tx, admission managedJournalAdmission, generation uint64) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,nonce,backend,name,body,phase,pid,pgid,cleanup FROM managed_scopes ORDER BY id")
	if err != nil {
		return false, fmt.Errorf("query reservation scope retirement: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		eligible, err := readReservationScope(rows, admission, generation)
		if err != nil || !eligible {
			return false, err
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read reservation scope retirement: %w", err)
	}
	return true, nil
}

func readReservationScope(rows *sql.Rows, admission managedJournalAdmission, generation uint64) (bool, error) {
	var id, nonce, backend, name, phase, cleanup string
	var body []byte
	var pid, pgid int
	if err := rows.Scan(&id, &nonce, &backend, &name, &body, &phase, &pid, &pgid, &cleanup); err != nil {
		return false, fmt.Errorf("decode reservation scope row: %w", err)
	}
	var identity struct {
		ScopeHandle
		Deadline time.Time
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		return false, fmt.Errorf("decode reservation scope identity: %w", err)
	}
	canonical, err := json.Marshal(identity)
	if err != nil || !bytes.Equal(canonical, body) {
		return false, errors.New("reservation scope identity is not its canonical encoding")
	}
	if err := validateManagedScope(identity.ScopeHandle); err != nil {
		return false, err
	}
	if identity.ID != id || identity.Nonce != nonce || identity.Backend != backend || identity.Name != name || identity.Generation > generation {
		return false, errors.New("reservation scope columns differ from exact ownership identity")
	}
	bound := admission.Deadline
	if identity.Kind == "observer" || identity.Kind == "keeper" {
		bound = admission.HardStop
	}
	if identity.Deadline.IsZero() || identity.Deadline.After(bound) {
		return false, errors.New("reservation scope lifetime is invalid")
	}
	return reservationPhaseQuiescent(phase, cleanup, pid, pgid)
}

func reservationPhaseQuiescent(phase, cleanup string, pid, pgid int) (bool, error) {
	if !validReservationPeer(pid, pgid) {
		return false, errors.New("reservation scope authenticated peer is invalid")
	}
	switch phase {
	case "reserved", "granted", "revoked":
		return false, nil
	case "quiescent":
		if cleanup == "" || len(cleanup) > 256 {
			return false, errors.New("reservation scope has no bounded positive cleanup evidence")
		}
		return true, nil
	default:
		return false, errors.New("reservation scope has an invalid lifecycle phase")
	}
}

func validReservationPeer(pid, pgid int) bool {
	return (pid == 0 && pgid == 0) || (pid > 1 && pgid > 1)
}
