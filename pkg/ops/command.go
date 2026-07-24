package ops

import (
	"bytes"
	"context"
	"fmt"

	"oro/pkg/storage"
)

// runOpsCommand executes a short-lived direct ops command through the shared
// lease-aware command runner. newRuntime must return a unique request for each
// command because concurrent children cannot share a lease identity.
func runOpsCommand(ctx context.Context, newRuntime func() storage.RuntimeRequest, path string, args []string, workdir string) (string, error) {
	if newRuntime == nil {
		return "", fmt.Errorf("ops runtime request factory is nil")
	}

	var stdout bytes.Buffer
	runtime := newRuntime()
	runtime.Workdir = workdir
	_, err := storage.RunLeasedCommand(ctx, storage.CommandRequest{
		Runtime: runtime,
		Path:    path,
		Args:    args,
		Dir:     workdir,
		Stdout:  &stdout,
		Stderr:  &stdout,
	})
	if err != nil {
		return "", fmt.Errorf("run ops command %q: %w", path, err)
	}
	return stdout.String(), nil
}
