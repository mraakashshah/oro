package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// MutationPolicy defines how a task command behaves when it is invoked from a
// worker-owned process. Policies are intentionally explicit so adding a Cobra
// leaf requires choosing between human-only mutation, dispatcher routing, and
// read-only access.
type MutationPolicy string

const (
	MutationPolicyUnknown  MutationPolicy = "unknown"
	MutationPolicyReadOnly MutationPolicy = "read-only"
	MutationPolicyDeny     MutationPolicy = "deny"
	MutationPolicyRouteIPC MutationPolicy = "route-ipc"
)

// workerMutationPolicy returns the explicit worker policy for a Cobra leaf.
// An unlisted command is unknown by design: the Cobra-tree regression catches
// it before a new task command can silently mutate worker-owned state.
func workerMutationPolicy(cmd *cobra.Command) MutationPolicy {
	if cmd == nil {
		return MutationPolicyUnknown
	}
	switch cmd.CommandPath() {
	case "task ready", "task list", "task show", "task blocked", "task closed", "task dep list", "task dep cycles", "task export", "task status":
		return MutationPolicyReadOnly
	case "task create", "task update", "task close", "task delete", "task reopen", "task defer", "task undefer", "task dep add", "task dep rm", "task note add":
		return MutationPolicyDeny
	case "task propose-blocker":
		return MutationPolicyRouteIPC
	default:
		return MutationPolicyUnknown
	}
}

func guardTaskWorkerMutation(cmd *cobra.Command) error {
	if !hasWorkerMutationIdentity() || len(cmd.Commands()) > 0 {
		return nil
	}

	switch workerMutationPolicy(cmd) {
	case MutationPolicyReadOnly, MutationPolicyRouteIPC:
		return nil
	case MutationPolicyDeny:
		return fmt.Errorf("worker identity present: refusing direct task mutation %q; route the request through the dispatcher", cmd.CommandPath())
	default:
		return fmt.Errorf("worker identity present: task command %q has no mutation policy", cmd.CommandPath())
	}
}

// hasWorkerMutationIdentity deliberately does not rely on ORO_WORKER alone.
// A missing marker with any assignment identity is still worker context and
// must fail closed rather than granting direct store mutation access.
func hasWorkerMutationIdentity() bool {
	for _, key := range []string{"ORO_WORKER", "ORO_WORKER_ID", "ORO_WORKER_BEAD_ID", "ORO_CAPABILITY_FILE"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}
