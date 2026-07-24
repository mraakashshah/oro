package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"oro/pkg/protocol"
)

// WorkerExecutionContext is the assignment authority scoped to one subprocess.
// It must be carried through the spawn context rather than process-global
// environment mutation so a reassignment cannot leak identity into another run.
//
//nolint:revive // public task contract requires the WorkerExecutionContext name.
type WorkerExecutionContext struct {
	AssignmentID   int64
	Generation     int64
	WorkerID       string
	Role           string
	SocketPath     string
	CapabilityFile string
}

type (
	workerExecutionContextKey struct{}
	assignmentBeadIDKey       struct{}
)

// WithExecutionContext binds assignment authority to a child-process context.
func WithExecutionContext(ctx context.Context, execution WorkerExecutionContext) context.Context {
	return context.WithValue(ctx, workerExecutionContextKey{}, execution)
}

// ExecutionContextFrom returns the assignment authority bound to ctx.
func ExecutionContextFrom(ctx context.Context) (WorkerExecutionContext, bool) {
	execution, ok := ctx.Value(workerExecutionContextKey{}).(WorkerExecutionContext)
	return execution, ok
}

func withAssignmentContext(ctx context.Context, execution WorkerExecutionContext, beadID string) context.Context {
	ctx = WithExecutionContext(ctx, execution)
	return context.WithValue(ctx, assignmentBeadIDKey{}, beadID)
}

func executionContextForAssign(a *protocol.AssignPayload, workerID, socketPath string) (WorkerExecutionContext, error) {
	if a.AssignmentID <= 0 {
		return WorkerExecutionContext{}, fmt.Errorf("assign execution context missing assignment ID")
	}
	if a.Generation <= 0 {
		return WorkerExecutionContext{}, fmt.Errorf("assign execution context missing generation")
	}
	if strings.TrimSpace(a.ActorRole) == "" {
		return WorkerExecutionContext{}, fmt.Errorf("assign execution context missing role")
	}
	if strings.TrimSpace(workerID) == "" {
		return WorkerExecutionContext{}, fmt.Errorf("assign execution context missing worker ID")
	}
	execution := WorkerExecutionContext{
		AssignmentID: a.AssignmentID,
		Generation:   a.Generation,
		WorkerID:     workerID,
		Role:         a.ActorRole,
		SocketPath:   socketPath,
	}
	if a.Capability != "" {
		execution.CapabilityFile = filepath.Join(a.Worktree, protocol.OroDir, "assignment-capability.json")
	}
	return execution, nil
}

// EnvironmentForExecution replaces assignment-owned environment entries with
// values from execution. It never mutates the parent process environment.
func EnvironmentForExecution(env []string, execution WorkerExecutionContext) []string {
	return environmentForExecution(env, execution, "")
}

// EnvironmentForContext creates a child-process environment from assignment
// authority stored on ctx. It is intentionally separate from os.Environ so
// reassignment cannot mutate or leak the parent worker's identity.
func EnvironmentForContext(ctx context.Context, env []string) []string {
	execution, _ := ExecutionContextFrom(ctx)
	beadID, _ := ctx.Value(assignmentBeadIDKey{}).(string)
	return environmentForExecution(env, execution, beadID)
}

func environmentForExecution(env []string, execution WorkerExecutionContext, beadID string) []string {
	const prefix = "ORO_"
	owned := map[string]string{
		"ORO_ASSIGNMENT_ID":         strconv.FormatInt(execution.AssignmentID, 10),
		"ORO_ASSIGNMENT_GENERATION": strconv.FormatInt(execution.Generation, 10),
		"ORO_WORKER_ID":             execution.WorkerID,
		"ORO_ROLE":                  execution.Role,
		"ORO_SOCKET_PATH":           execution.SocketPath,
		"ORO_CAPABILITY_FILE":       execution.CapabilityFile,
		"ORO_WORKER_BEAD_ID":        beadID,
	}
	filtered := make([]string, 0, len(env)+len(owned))
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(key, prefix) {
			if _, replaces := owned[key]; replaces {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	for _, key := range []string{"ORO_ASSIGNMENT_ID", "ORO_ASSIGNMENT_GENERATION", "ORO_WORKER_ID", "ORO_ROLE", "ORO_SOCKET_PATH", "ORO_CAPABILITY_FILE", "ORO_WORKER_BEAD_ID"} {
		if value := owned[key]; value != "" {
			filtered = append(filtered, key+"="+value)
		}
	}
	return filtered
}
