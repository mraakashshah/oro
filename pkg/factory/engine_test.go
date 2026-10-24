package factory //nolint:testpackage // Exercise private recovery state alongside the public controller.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFactoryAutonomousDeliveryRecovery(t *testing.T) {
	t.Run("repair delivered behavior", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		initial := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch)
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "completed" || status.Attempts != 2 {
			t.Fatalf("automatic repair: status=%+v err=%v", status, err)
		}
		if status.CandidateSHA == initial || engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch) != status.CandidateSHA {
			t.Fatal("delivery did not promote the accepted candidate")
		}
		fresh := filepath.Join(t.TempDir(), "delivered")
		engineGit(t, spec.Repository, "worktree", "add", "--detach", fresh, status.CandidateSHA)
		for _, args := range [][]string{{"2", "3"}, {"-4", "3"}} {
			want := "5"
			if args[0] == "-4" {
				want = "-1"
			}
			out, err := exec.CommandContext(t.Context(), "sh", append([]string{filepath.Join(fresh, "add.sh")}, args...)...).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != want {
				t.Fatalf("delivered artifact %v: %q, %v", args, out, err)
			}
		}
		repeat, err := Run(t.Context(), state, spec)
		if err != nil || repeat != status {
			t.Fatalf("terminal result changed: %+v, %v", repeat, err)
		}
		read, err := ReadStatus(t.Context(), state, spec.ID)
		if err != nil || read != status {
			t.Fatalf("durable status: %+v, %v", read, err)
		}
	})
	t.Run("budget stays exhausted", func(t *testing.T) {
		spec, state := engineFixture(t, "reject")
		spec.MaxAttempts = 2
		initial := engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch)
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "failed" || status.Attempts != 2 || status.Reason == "" {
			t.Fatalf("bounded rejection: %+v, %v", status, err)
		}
		repeat, err := Run(t.Context(), state, spec)
		if err != nil || repeat != status || engineGit(t, spec.Repository, "rev-parse", spec.TargetBranch) != initial {
			t.Fatalf("failed objective reopened or promoted: %+v, %v", repeat, err)
		}
		changed := spec
		changed.MaxAttempts++
		if _, err := Run(t.Context(), state, changed); err == nil {
			t.Fatal("resumed objective accepted a larger budget")
		}
	})
	t.Run("public controllers exclude each other", engineControllerExclusion)
}

func engineControllerExclusion(t *testing.T) {
	t.Helper()
	spec, state := engineFixture(t, "block-first")
	marker := spec.Worker.Args[0]
	specFile := filepath.Join(t.TempDir(), "spec.json")
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	engineWrite(t, specFile, string(data), 0o600)
	owner := engineController(t, state, specFile)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	var workerPID int
	for time.Now().Before(deadline) {
		if b, readErr := os.ReadFile(marker); readErr == nil {
			workerPID, err = strconv.Atoi(string(b))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workerPID <= 0 {
		t.Fatal("first controller never started its worker")
	}
	t.Cleanup(func() { _ = syscall.Kill(-workerPID, syscall.SIGKILL) })
	before, err := readSnapshot(t.Context(), filepath.Join(state, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	loser := engineController(t, state, specFile)
	out, err := loser.CombinedOutput()
	if err == nil {
		t.Fatalf("second public controller acquired objective: %s", out)
	}
	after, err := readSnapshot(t.Context(), filepath.Join(state, spec.ID))
	if err != nil || after.Revision != before.Revision || after.Attempts != before.Attempts {
		t.Fatalf("loser mutated durable state: before=%+v after=%+v err=%v", before, after, err)
	}
	starts, err := os.ReadFile(marker + ".starts")
	if err != nil || string(starts) != "worker\n" {
		t.Fatalf("loser invoked adapter: %q, %v", starts, err)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	// This increment delegates orphan containment to the external execution boundary.
	// Clean up the fixture worker explicitly; the OS releases the controller's lock.
	_ = syscall.Kill(-workerPID, syscall.SIGKILL)
	status, err := Run(t.Context(), state, spec)
	if err != nil || status.State != "completed" || status.Attempts != 2 {
		t.Fatalf("owner death did not permit bounded resume: %+v, %v", status, err)
	}
}

func engineController(t *testing.T, state, specFile string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestFactoryControllerProcess$", "--", state, specFile)
	cmd.Env = engineEnv()
	return cmd
}

func TestFactoryControllerProcess(t *testing.T) {
	args := engineProcessArgs()
	if len(args) != 2 {
		return
	}
	b, err := os.ReadFile(args[1])
	var spec Spec
	if err == nil {
		err = json.Unmarshal(b, &spec)
	}
	if err == nil {
		_, err = Run(t.Context(), args[0], spec)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestFactoryPolicyProcess(t *testing.T) {
	args := engineProcessArgs()
	if len(args) != 3 {
		return
	}
	var input commandInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var err error
	switch args[0] {
	case "worker":
		err = enginePolicyWorker(input, args[1], args[2])
	case "acceptance":
		err = enginePolicyAccept(input)
	case "review":
		_, err = fmt.Printf(`{"candidate_sha":%q,"approved":true,"feedback":""}`, input.CandidateSHA)
	default:
		err = fmt.Errorf("unknown fixture policy %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func enginePolicyWorker(input commandInput, mode, marker string) error {
	if mode == "block-first" {
		if err := engineBlockFirstWorker(input.Attempt, marker); err != nil {
			return err
		}
	}
	operator := "-"
	if input.Attempt > 1 && mode != "reject" {
		if input.Feedback == "" {
			return fmt.Errorf("repair attempt did not receive feedback")
		}
		operator = "+"
	}
	if err := os.WriteFile("add.sh", []byte("#!/bin/sh\necho $(( $1 "+operator+" $2 ))\n"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll("build", 0o700); err != nil {
		return err
	}
	return os.WriteFile("build/add.sh", []byte("#!/bin/sh\necho $(( $1 + $2 ))\n"), 0o700)
}

func engineBlockFirstWorker(attempt int, marker string) error {
	f, err := os.OpenFile(marker+".starts", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("worker\n")
	_ = f.Close()
	if err != nil || attempt != 1 {
		return err
	}
	if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	time.Sleep(30 * time.Second)
	return nil
}

func enginePolicyAccept(input commandInput) error {
	program := "add.sh"
	// This deliberately unsafe cache preference proves that pristine checkouts matter.
	if _, err := os.Stat("build/add.sh"); err == nil {
		program = "build/add.sh"
	}
	passed := true
	for _, check := range [][]string{{"2", "3", "5"}, {"-4", "3", "-1"}} {
		out, err := exec.CommandContext(context.Background(), "sh", program, check[0], check[1]).CombinedOutput()
		passed = passed && err == nil && strings.TrimSpace(string(out)) == check[2]
	}
	feedback := ""
	if !passed {
		feedback = "expected integer addition: 2+3=5 and -4+3=-1"
	}
	_, err := fmt.Printf(`{"candidate_sha":%q,"passed":%t,"checks":["positive-addition","negative-addition"],"feedback":%q}`, input.CandidateSHA, passed, feedback)
	return err
}

func engineFixture(t *testing.T, mode string) (Spec, string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	engineGit(t, repo, "init", "-b", "main")
	engineWrite(t, filepath.Join(repo, ".gitignore"), "build/\n", 0o600)
	engineGit(t, repo, "add", ".")
	engineGit(t, repo, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "initial")
	engineGit(t, repo, "branch", "delivery")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "worker-started")
	programs := map[string]Command{}
	for _, stage := range []string{"worker", "acceptance", "review"} {
		path := filepath.Join(dir, stage)
		script := "#!/bin/sh\nexec " + engineQuote(binary) + " -test.run='^TestFactoryPolicyProcess$' -- " + engineQuote(stage) + " " + engineQuote(mode) + " \"$1\"\n"
		engineWrite(t, path, script, 0o700)
		programs[stage] = Command{Path: path, Args: []string{marker}}
	}
	return Spec{
		ID: "addition", Objective: "Implement addition of positive and negative integers", Repository: repo,
		TargetBranch: "delivery", Worker: programs["worker"], Acceptance: programs["acceptance"], Review: programs["review"],
		RequiredChecks: []string{"positive-addition", "negative-addition"}, MaxAttempts: 3, TimeoutSeconds: 60, StepTimeoutSeconds: 15,
	}, filepath.Join(dir, "state")
}

func engineProcessArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

func engineQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func engineGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}, args...)...)
	cmd.Dir, cmd.Env = dir, engineEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s, %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func engineEnv() []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != "ORO" && !strings.HasPrefix(key, "ORO_") && !strings.HasPrefix(key, "GIT_") {
			env = append(env, item)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

func engineWrite(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}
