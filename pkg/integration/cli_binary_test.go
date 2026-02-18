package integration_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildOroBinary compiles the oro binary into a temp directory and returns
// the path to the compiled binary. Build failure is a hard fatal (not a skip),
// so CI catches regressions immediately.
func buildOroBinary(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping CLI binary smoke tests in short mode")
	}

	root := integrationProjectRoot(t)

	// Stage embedded assets required by go:embed (same as `make stage-assets`).
	stageAssets(t, root)

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "oro")

	build := exec.Command("go", "build", "-o", binPath, "./cmd/oro") //nolint:gosec // test-only, args are constant
	build.Dir = root
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/oro failed: %v\n%s", err, out)
	}

	return binPath
}

// integrationProjectRoot walks up from the package directory to find go.mod.
func integrationProjectRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

// stageAssets runs `make stage-assets` from the project root so that the
// embed.FS in cmd/oro/embed.go has a populated _assets/ directory at compile time.
// It registers a cleanup to remove the staged directory after the test.
func stageAssets(t *testing.T, projectRoot string) {
	t.Helper()

	assetsDir := filepath.Join(projectRoot, "cmd", "oro", "_assets")

	// Skip staging if already present (e.g., running under `make test`).
	if _, err := os.Stat(assetsDir); err == nil {
		return
	}

	stage := exec.Command("make", "stage-assets") //nolint:gosec // test-only, constant args
	stage.Dir = projectRoot
	out, err := stage.CombinedOutput()
	if err != nil {
		t.Fatalf("make stage-assets failed: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		if err := os.RemoveAll(assetsDir); err != nil {
			t.Logf("warning: failed to clean up _assets: %v", err)
		}
	})
}

// TestOroBinary_AllSubcommandsHelp verifies that every top-level and nested
// subcommand responds to --help with exit code 0 and non-empty stdout.
//
// The 26 subcommands under test match the acceptance criteria:
// oro, start, stop, status, dispatcher, dispatcher start, dispatcher stop,
// worker, worker launch, worker stop, logs, cleanup, init, remember, recall,
// forget, ingest, memories, memories list, memories consolidate,
// index, index build, index search, work, dash, directive.
func TestOroBinary_AllSubcommandsHelp(t *testing.T) {
	binPath := buildOroBinary(t)

	subcommands := [][]string{
		{"--help"},
		{"start", "--help"},
		{"stop", "--help"},
		{"status", "--help"},
		{"dispatcher", "--help"},
		{"dispatcher", "start", "--help"},
		{"dispatcher", "stop", "--help"},
		{"worker", "--help"},
		{"worker", "launch", "--help"},
		{"worker", "stop", "--help"},
		{"logs", "--help"},
		{"cleanup", "--help"},
		{"init", "--help"},
		{"remember", "--help"},
		{"recall", "--help"},
		{"forget", "--help"},
		{"ingest", "--help"},
		{"memories", "--help"},
		{"memories", "list", "--help"},
		{"memories", "consolidate", "--help"},
		{"index", "--help"},
		{"index", "build", "--help"},
		{"index", "search", "--help"},
		{"work", "--help"},
		{"dash", "--help"},
		{"directive", "--help"},
	}

	for _, args := range subcommands {
		args := args // capture range variable
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command(binPath, args...) //nolint:gosec // test-only
			out, err := cmd.Output()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					t.Fatalf("oro %s exited non-zero (%d)\nstdout: %s\nstderr: %s",
						name, exitErr.ExitCode(), out, exitErr.Stderr)
				}
				t.Fatalf("oro %s failed: %v\nstdout: %s", name, err, out)
			}
			if len(out) == 0 {
				t.Errorf("oro %s: expected non-empty stdout, got empty", name)
			}
		})
	}
}

// TestOroBinary_DaemonRequired_Errors verifies that commands requiring a
// running dispatcher daemon exit non-zero and produce meaningful error output
// when no daemon is present.
func TestOroBinary_DaemonRequired_Errors(t *testing.T) {
	binPath := buildOroBinary(t)

	// Use a temp dir with a known socket path so the error output mentions "socket".
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "test.socket")

	cases := []struct {
		name string
		args []string
	}{
		// oro directive requires a live dispatcher socket to send directives.
		// Without a daemon, it must exit non-zero and mention the socket.
		{
			name: "directive_no_daemon",
			args: []string{"directive", "test"},
		},
		// oro status exits non-zero when the dispatcher socket path is set
		// and the socket file does not exist (socket-first liveness check fails
		// and there is no PID file fallback in this isolated env).
		{
			name: "status_no_daemon",
			args: []string{"status"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command(binPath, tc.args...) //nolint:gosec // test-only
			// Point to our temp socket path so errors mention a recognizable socket name.
			cmd.Env = append(os.Environ(),
				"ORO_SOCKET_PATH="+sockPath,
				"ORO_PID_PATH="+filepath.Join(sockDir, "oro.pid"),
			)
			// Capture combined output so we can search both stdout and stderr.
			combined, err := cmd.CombinedOutput()

			lower := strings.ToLower(string(combined))
			invocation := strings.Join(tc.args, " ")

			switch tc.name {
			case "directive_no_daemon":
				// directive must exit non-zero and mention socket connectivity.
				if err == nil {
					t.Fatalf("oro %s: expected non-zero exit but got 0\noutput: %s",
						invocation, combined)
				}
				if !strings.Contains(lower, "socket") && !strings.Contains(lower, "connection refused") &&
					!strings.Contains(lower, "sock") {
					t.Errorf("oro %s: expected output to mention socket issue\ngot: %s",
						invocation, combined)
				}

			case "status_no_daemon":
				// status exits 0 with "stopped" (graceful) OR non-zero with socket error.
				// Either way it must not crash and must produce meaningful output.
				if len(combined) == 0 {
					t.Errorf("oro %s: expected non-empty output, got empty", invocation)
				}
				if err != nil {
					// Non-zero exit: output must explain why.
					if !strings.Contains(lower, "socket") && !strings.Contains(lower, "sock") &&
						!strings.Contains(lower, "connection") && !strings.Contains(lower, "stopped") {
						t.Errorf("oro %s: non-zero exit but output doesn't explain state\ngot: %s",
							invocation, combined)
					}
				} else {
					// Zero exit: output should say "stopped".
					if !strings.Contains(lower, "stopped") {
						t.Errorf("oro %s: zero exit but output missing 'stopped'\ngot: %s",
							invocation, combined)
					}
				}
			}
		})
	}
}
