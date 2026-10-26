package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	root, view, ref := factoryServiceFixture(t, 30)
	specID := view.ObjectiveID
	access, err := lockFactoryService(t.Context(), root, specID)
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
	service := filepath.Join(root, specID, "service")
	stage := filepath.Join(root, specID, ".service.stage")
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

func factoryServicePlistCommandTest(t *testing.T) {
	intent := factoryServiceIntent{Root: "/state", ID: "example", OroPath: "/state/example/oro", Runtime: factory.ManagedRuntimeReference{Path: "/state/example/runtime/runtime.json", SHA256: strings.Repeat("a", 64)}, Label: "dev.getoro.factory.test.supervisor.nonce", HardStop: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	data, err := factoryServicePlist(intent)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Dict struct {
			Keys   []string `xml:"key"`
			Arrays []struct {
				Values []string `xml:"string"`
			} `xml:"array"`
		} `xml:"dict"`
	}
	assertFactoryServicePlistProperties(t, data, intent)
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Dict.Arrays) != 1 {
		t.Fatalf("plist arrays: %+v", document.Dict.Arrays)
	}
	argv := document.Dict.Arrays[0].Values
	want := []string{intent.OroPath, "factory", "_supervise", "--state-dir", intent.Root, "--id", intent.ID, "--runtime-config", intent.Runtime.Path, "--runtime-config-sha256", intent.Runtime.SHA256, "--hard-stop", intent.HardStop.Format(time.RFC3339Nano)}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("plist argv = %q, want %q", argv, want)
	}
	root := newRootCmd()
	cmd, rest, err := root.Find(argv[1:])
	if err != nil || cmd.CommandPath() != "oro factory _supervise" {
		t.Fatalf("command resolution: %v %v", cmd, err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"state-dir": intent.Root, "id": intent.ID, "runtime-config": intent.Runtime.Path, "runtime-config-sha256": intent.Runtime.SHA256, "hard-stop": intent.HardStop.Format(time.RFC3339Nano)} {
		got, err := cmd.Flags().GetString(name)
		if err != nil || got != value {
			t.Fatalf("flag %s = %q: %v", name, got, err)
		}
	}
}

func TestFactoryServiceState(t *testing.T) {
	t.Run("plist command", factoryServicePlistCommandTest)
	t.Run("deadline", factoryServiceDeadlineTest)
	t.Run("artifacts", factoryServiceArtifactsTest)
	t.Run("lock aliases", factoryServiceLockAliasesTest)
	t.Run("lock", factoryServiceLockTest)
	t.Run("uncommitted", factoryServiceRejectsUncommittedAdmission)
	t.Run("prepared", factoryServicePreparedBundle)
}

func factoryServiceFixture(t *testing.T, timeout int) (string, factory.ManagedAdmissionView, factory.ManagedRuntimeReference) {
	t.Helper()

	root, spec := factoryBudgetFixture(t)
	spec.TimeoutSeconds, spec.StepTimeoutSeconds = timeout, 1
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

	return root, view, ref
}

func factoryServiceDeadlineTest(t *testing.T) {
	root, view, ref := factoryServiceFixture(t, 3)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	objective, lock, err := openFactoryPublication(t.Context(), view, false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := access.prepare(t.Context(), view, ref); done <- err }()
	time.Sleep(time.Until(view.Deadline) + 50*time.Millisecond)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := objective.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("expired publication accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, view.ObjectiveID, "service")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service published after deadline: %v", err)
	}
}

func factoryServiceArtifactsTest(t *testing.T) {
	root, view, ref := factoryServiceFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	intent, _, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(root, view.ObjectiveID, "service")
	stage := filepath.Join(root, view.ObjectiveID, ".service.stage")
	intentPath := filepath.Join(service, "intent.json")
	alias := filepath.Join(root, view.ObjectiveID, "intent-alias")
	if err := os.Link(intentPath, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("hard linked intent accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(service, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("nonprivate bundle accepted")
	}
	if err := os.Chmod(service, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(service, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alias, service); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("symlink bundle accepted")
	}
	if err := os.Remove(service); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(alias, service); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(service, stage); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(stage, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(stage, "intent.json"), alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("hard linked stage accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(stage, "service.plist")
	if err := os.Rename(plist, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("incomplete stage accepted")
	}
	if _, err := os.Lstat(service); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete stage published")
	}
	if err := os.Symlink(alias, plist); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("symlink stage accepted")
	}
	if _, err := os.Lstat(service); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt stage published: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(stage, "intent.json"))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("corrupt stage identity changed")
	}
	if err := os.Remove(plist); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(alias, plist); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.prepare(t.Context(), view, ref); err == nil {
		t.Fatal("nonprivate stage accepted")
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := access.prepare(t.Context(), view, ref)
	if err != nil || recovered != intent {
		t.Fatalf("stage recovery: %v", err)
	}
}

func factoryServiceLockAliasesTest(t *testing.T) {
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
	if err := access.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".managed", "locks", "objective", "service.lock")
	alias := filepath.Join(root, "alias")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := lockFactoryService(t.Context(), root, "objective"); err == nil {
		t.Fatal("hard linked lock accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockFactoryService(t.Context(), root, "objective"); err == nil {
		t.Fatal("nonprivate lock accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alias, path); err != nil {
		t.Fatal(err)
	}
	if _, err := lockFactoryService(t.Context(), root, "objective"); err == nil {
		t.Fatal("symlink lock accepted")
	}
}

type factoryServiceTestXML struct {
	XMLName  xml.Name
	Text     string                  `xml:",chardata"`
	Children []factoryServiceTestXML `xml:",any"`
}

func assertFactoryServicePlistProperties(t *testing.T, data []byte, intent factoryServiceIntent) {
	t.Helper()
	var doc factoryServiceTestXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.XMLName.Local != "plist" || len(doc.Children) != 1 || doc.Children[0].XMLName.Local != "dict" {
		t.Fatal("invalid plist envelope")
	}
	children := doc.Children[0].Children
	if len(children) != 24 {
		t.Fatalf("plist entries: %d", len(children))
	}
	values := map[string]factoryServiceTestXML{}
	for i := 0; i < len(children); i += 2 {
		if children[i].XMLName.Local != "key" {
			t.Fatal("invalid plist key")
		}
		values[children[i].Text] = children[i+1]
	}
	expected := map[string][2]string{"Label": {"string", intent.Label}, "Program": {"string", intent.OroPath}, "WorkingDirectory": {"string", intent.Root}, "RunAtLoad": {"true", ""}, "ThrottleInterval": {"integer", "30"}, "ExitTimeOut": {"integer", "5"}, "Umask": {"integer", "63"}, "StandardInPath": {"string", "/dev/null"}, "StandardOutPath": {"string", "/dev/null"}, "StandardErrorPath": {"string", "/dev/null"}}
	for key, want := range expected {
		value, ok := values[key]
		if !ok || value.XMLName.Local != want[0] || value.Text != want[1] || len(value.Children) != 0 {
			t.Fatalf("plist %s = %+v", key, value)
		}
	}
	argv, ok := values["ProgramArguments"]
	if !ok || argv.XMLName.Local != "array" {
		t.Fatal("missing argv")
	}
	keep, ok := values["KeepAlive"]
	if !ok || keep.XMLName.Local != "dict" || len(keep.Children) != 2 || keep.Children[0].XMLName.Local != "key" || keep.Children[0].Text != "SuccessfulExit" || keep.Children[1].XMLName.Local != "false" {
		t.Fatalf("invalid keep alive: %+v", keep)
	}
}
