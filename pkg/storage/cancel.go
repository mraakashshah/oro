package storage

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const namespaceCancellationGrace = 30 * time.Second

// LeaseOwner identifies an active runtime lease owner that may be cancelled
// when its namespace exceeds the scratch limit.
type LeaseOwner struct {
	LeaseID   LeaseID
	Namespace string
	Identity  ProcessIdentity
}

// ProcessGroupSignaler sends a signal to every process in an owned group.
type ProcessGroupSignaler func(processGroup int, signal os.Signal) error

// NamespaceCanceller cancels verified lease owners without allowing a reused
// PID to authorize a signal.
type NamespaceCanceller struct {
	inspect ProcessInspector
	signal  ProcessGroupSignaler
	sleep   func(time.Duration)
}

// NewNamespaceCanceller creates a namespace cancellation coordinator. Nil
// dependencies use the host process inspector, process-group signaler, and
// wall-clock sleep.
//
//oro:testonly — pressure-policy wiring lands in dependent runtime lifecycle tasks.
func NewNamespaceCanceller(inspect ProcessInspector, signal ProcessGroupSignaler, sleep func(time.Duration)) *NamespaceCanceller {
	if inspect == nil {
		inspect = InspectProcessIdentity
	}
	if signal == nil {
		signal = signalProcessGroup
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return &NamespaceCanceller{inspect: inspect, signal: signal, sleep: sleep}
}

// CancelOversizedNamespace gracefully cancels every verified owner in
// namespace. Outside critical pressure it gives owners thirty seconds to exit
// before terminating only identities that still match their original lease.
//
//oro:testonly — pressure-policy wiring lands in dependent runtime lifecycle tasks.
func (c *NamespaceCanceller) CancelOversizedNamespace(namespace string, owners []LeaseOwner, critical bool) error {
	if c == nil || c.inspect == nil || c.signal == nil || c.sleep == nil {
		return fmt.Errorf("invalid namespace canceller")
	}

	verified := c.verifiedOwners(namespace, owners)
	var errs []error
	for _, owner := range verified {
		if err := c.signal(owner.Identity.ProcessGroup, os.Interrupt); err != nil {
			errs = append(errs, fmt.Errorf("cancel lease %s process group %d: %w", owner.LeaseID, owner.Identity.ProcessGroup, err))
		}
	}
	if !critical && len(verified) > 0 {
		c.sleep(namespaceCancellationGrace)
	}
	for _, owner := range verified {
		if !c.matches(owner.Identity) {
			continue
		}
		if err := c.signal(owner.Identity.ProcessGroup, os.Kill); err != nil {
			errs = append(errs, fmt.Errorf("terminate lease %s process group %d: %w", owner.LeaseID, owner.Identity.ProcessGroup, err))
		}
	}
	return errors.Join(errs...)
}

func (c *NamespaceCanceller) verifiedOwners(namespace string, owners []LeaseOwner) []LeaseOwner {
	verified := make([]LeaseOwner, 0, len(owners))
	for _, owner := range owners {
		if owner.Namespace == namespace && c.matches(owner.Identity) {
			verified = append(verified, owner)
		}
	}
	return verified
}

func (c *NamespaceCanceller) matches(owner ProcessIdentity) bool {
	live, err := c.inspect(owner.PID)
	return err == nil && owner.Matches(live)
}
