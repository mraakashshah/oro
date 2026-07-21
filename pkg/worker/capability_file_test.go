package worker_test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/worker"
)

func TestLiveCapabilityFileLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assignment-capability.json")
	first := worker.AssignmentCredential{
		AssignmentID: 41,
		Generation:   1,
		CapabilityID: "capability-one",
		Token:        "token-one",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Round(0),
	}
	if err := worker.ReplaceCapabilityFile(path, first); err != nil {
		t.Fatalf("create capability file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat capability file: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("capability file mode = %o, want %o", got, want)
	}

	stdin, stdout := startCapabilityFileAgentShim(t, path)
	if got := requestCredential(t, stdin, stdout); got != first.Token {
		t.Fatalf("initial credential = %q, want %q", got, first.Token)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen capability file mode: %v", err)
	}
	if got := requestCredential(t, stdin, stdout); got != "ERROR" {
		t.Fatalf("credential with unsafe mode = %q, want ERROR", got)
	}

	second := first
	second.CapabilityID = "capability-two"
	second.Token = "token-two"
	second.ExpiresAt = first.ExpiresAt.Add(time.Minute)
	if err := worker.ReplaceCapabilityFile(path, second); err != nil {
		t.Fatalf("replace capability file: %v", err)
	}
	if got := requestCredential(t, stdin, stdout); got != second.Token {
		t.Fatalf("replacement credential = %q, want %q", got, second.Token)
	}

	if err := worker.RemoveCapabilityFile(path); err != nil {
		t.Fatalf("remove terminal capability file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("capability file after terminal removal: err = %v, want not exist", err)
	}
	if got := requestCredential(t, stdin, stdout); got != "ERROR" {
		t.Fatalf("credential after terminal removal = %q, want ERROR", got)
	}
}

func startCapabilityFileAgentShim(t *testing.T, path string) (io.WriteCloser, *bufio.Scanner) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCapabilityFileAgentShim$") //nolint:gosec // test binary is trusted
	cmd.Env = append(os.Environ(), "GO_WANT_CAPABILITY_FILE_AGENT_SHIM=1", "ORO_CAPABILITY_FILE="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("agent shim stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("agent shim stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent shim: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return stdin, bufio.NewScanner(stdout)
}

func requestCredential(t *testing.T, stdin io.Writer, stdout *bufio.Scanner) string {
	t.Helper()
	if _, err := fmt.Fprintln(stdin, "read"); err != nil {
		t.Fatalf("request credential: %v", err)
	}
	if !stdout.Scan() {
		t.Fatalf("read credential response: %v", stdout.Err())
	}
	return stdout.Text()
}

func TestCapabilityFileAgentShim(t *testing.T) {
	if os.Getenv("GO_WANT_CAPABILITY_FILE_AGENT_SHIM") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		credential, err := worker.ReadCapabilityFile(os.Getenv("ORO_CAPABILITY_FILE"))
		if err != nil {
			fmt.Println("ERROR")
			continue
		}
		fmt.Println(credential.Token)
	}
}
