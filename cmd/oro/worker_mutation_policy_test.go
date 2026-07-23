package main

import (
	"strings"
	"testing"

	"oro/pkg/beadstore"

	"github.com/spf13/cobra"
)

//nolint:testpackage // This white-box policy audit verifies every task Cobra leaf.
func TestWorkerMutationPolicyCoversCobraTree(t *testing.T) {
	t.Setenv("ORO_WORKER", "")
	t.Setenv("ORO_WORKER_ID", "worker-1")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-1")
	t.Setenv("ORO_CAPABILITY_FILE", "")

	mutableLeaves := map[string]struct{}{
		"task create":          {},
		"task update":          {},
		"task reopen":          {},
		"task close":           {},
		"task delete":          {},
		"task defer":           {},
		"task undefer":         {},
		"task note add":        {},
		"task dep add":         {},
		"task dep rm":          {},
		"task propose-blocker": {},
	}

	root := newTaskCmdWithStore(beadstore.NewFakeStore())
	walkCobraLeaves(root, func(cmd *cobra.Command) {
		path := cmd.CommandPath()
		policy := workerMutationPolicy(cmd)
		if policy == MutationPolicyUnknown {
			t.Errorf("%s has no explicit worker mutation policy", path)
		}

		if _, mutable := mutableLeaves[path]; mutable && policy != MutationPolicyDeny && policy != MutationPolicyRouteIPC {
			t.Errorf("%s policy = %s, want deny or route-ipc when worker identity is present", path, policy)
		}
		if _, mutable := mutableLeaves[path]; mutable {
			err := guardTaskWorkerMutation(cmd)
			if policy == MutationPolicyDeny && err == nil {
				t.Errorf("%s allowed a direct mutation with worker identity", path)
			}
			if policy == MutationPolicyRouteIPC && err != nil {
				t.Errorf("%s IPC route error = %v", path, err)
			}
		}
	})
}

func walkCobraLeaves(cmd *cobra.Command, visit func(*cobra.Command)) {
	children := cmd.Commands()
	if len(children) == 0 {
		visit(cmd)
		return
	}
	for _, child := range children {
		walkCobraLeaves(child, visit)
	}
}

func TestWorkerMutationPolicyFailsClosedForIdentityWithoutWorkerMarker(t *testing.T) {
	t.Setenv("ORO_WORKER", "")
	t.Setenv("ORO_WORKER_ID", "worker-1")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-1")
	t.Setenv("ORO_CAPABILITY_FILE", "")

	cmd := newTaskCmdWithStore(beadstore.NewFakeStore())
	cmd.SetArgs([]string{"create"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "worker identity") {
		t.Fatalf("task create error = %v, want worker identity denial", err)
	}
}

func TestWorkerMutationPolicyUnknownLeafIsExplicit(t *testing.T) {
	cmd := &cobra.Command{Use: "future-mutation"}
	if policy := workerMutationPolicy(cmd); policy != MutationPolicyUnknown {
		t.Fatalf("unknown command policy = %q, want %q", policy, MutationPolicyUnknown)
	}
}
