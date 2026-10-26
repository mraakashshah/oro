package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/factory"
)

func factoryServiceLockTest(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	access, err := lockFactoryService(t.Context(), root, "objective")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".managed", "locks", "objective", "service.lock")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "objective")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("objective created: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := lockFactoryService(ctx, root, "objective"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
	if err := access.Close(); err != nil {
		t.Fatal(err)
	}
	access, err = lockFactoryService(context.Background(), root, "objective")
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode changed: %v", err)
	}
}

func factoryServiceRejectsUncommittedAdmission(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	access, err := lockFactoryService(t.Context(), root, "objective")
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	_, _, err = access.prepare(t.Context(), factory.ManagedAdmissionView{StateRoot: root, ObjectiveID: "objective"}, factory.ManagedRuntimeReference{})
	if err == nil {
		t.Fatal("uncommitted admission accepted")
	}
}

func factoryServicePreparedBundle(t *testing.T) {
	root, spec := factoryBudgetFixture(t)
	for _, args := range [][]string{{"init", "-b", "main", spec.Repository}, {"-C", spec.Repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture"}, {"-C", spec.Repository, "branch", "factory/first"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	spec.Runtime.GitExecutable, err = filepath.EvalSymlinks(git)
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(filepath.Dir(root), "model")
	if err := os.WriteFile(model, []byte("model"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec.Runtime.ModelExecutable = model
	reservation := factory.HostReservation{ID: "reservation-1", StateRoot: root, ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes}
	if _, err := factory.AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	view, err := factory.ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	oro, docker := filepath.Join(filepath.Dir(root), "oro-input"), filepath.Join(filepath.Dir(root), "docker-input")
	for _, path := range []string{oro, docker} {
		if err := os.WriteFile(path, []byte("program"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	socketDir, err := os.MkdirTemp("/tmp", "oro-service-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketDir, err = filepath.EvalSymlinks(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	inputs := factoryRuntimeInputs{OroPath: oro, OroSHA256: factoryHostDigest([]byte("program")), DockerPath: docker, DockerSHA256: factoryHostDigest([]byte("program")), DockerEndpoint: "unix://" + socket}
	ref, err := publishFactoryRuntime(t.Context(), view, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	access, err := lockFactoryService(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	intent, reg, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Phase != "prepared" || intent.HardStop != view.HardStop {
		t.Fatalf("bad prepared state: %+v %+v", intent, reg)
	}
	again, reg2, err := access.prepare(t.Context(), view, ref)
	if err != nil || intent != again || reg != reg2 {
		t.Fatalf("replay: %+v %+v %v", again, reg2, err)
	}
	service := filepath.Join(root, spec.ID, "service")
	stage := filepath.Join(root, spec.ID, ".service.stage")
	if err := os.Rename(service, stage); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := access.prepare(t.Context(), view, ref)
	if err != nil || recovered != intent {
		t.Fatalf("stage recovery changed identity: %+v %v", recovered, err)
	}
	bad := intent
	bad.Nonce = strings.Repeat("0", 32)
	if err := access.transition(t.Context(), bad, "prepared", "dispatching"); err == nil {
		t.Fatal("mismatched identity accepted")
	}
	plist, err := os.ReadFile(filepath.Join(service, "service.plist"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := factoryServicePlist(intent)
	if err != nil || !bytes.Equal(plist, expected) || !bytes.Contains(plist, []byte("<key>SuccessfulExit</key>")) {
		t.Fatal("incorrect plist")
	}
	if err := access.transition(t.Context(), intent, "prepared", "dispatching"); err != nil {
		t.Fatal(err)
	}
	if err := access.transition(t.Context(), intent, "prepared", "dispatching"); err == nil {
		t.Fatal("repeated transition accepted")
	}
	if err := access.transition(t.Context(), intent, "dispatching", "registered"); err != nil {
		t.Fatal(err)
	}
	_, reg, err = access.prepare(t.Context(), view, ref)
	if err != nil || reg.Phase != "registered" {
		t.Fatalf("registered replay: %+v %v", reg, err)
	}
	registrationPath := filepath.Join(service, "registration.json")
	original, err := os.ReadFile(registrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registrationPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registrationPath, append(original, ' '), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registrationPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("noncanonical registration accepted")
	}
	if err := os.Chmod(registrationPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registrationPath, original, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registrationPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service, ".registration.stage"), original, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("interrupted transition accepted")
	}
	if err := os.Remove(filepath.Join(service, ".registration.stage")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registrationPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registrationPath, bytes.Repeat([]byte("x"), (16<<10)+1), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registrationPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("oversized registration accepted")
	}
}

func TestFactoryServiceState(t *testing.T) {
	t.Run("lock", factoryServiceLockTest)
	t.Run("uncommitted", factoryServiceRejectsUncommittedAdmission)
	t.Run("prepared", factoryServicePreparedBundle)
}
