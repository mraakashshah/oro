package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryRuntimePublication(t *testing.T) {
	if raw := os.Getenv("ORO_PUBLICATION_CHILD"); raw != "" {
		var payload struct {
			View   factory.ManagedAdmissionView
			Inputs factoryRuntimeInputs
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		if _, err := publishFactoryRuntime(t.Context(), payload.View, payload.Inputs); err != nil {
			t.Fatal(err)
		}
		return
	}

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
	_, digest, err := factory.PrepareManagedSpecIdentity(spec)
	if err != nil {
		t.Fatal(err)
	}
	reservation := factory.HostReservation{ID: "reservation-1", StateRoot: root, ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes, SpecSHA256: digest}
	if _, err := factory.AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	view, err := factory.ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	oro := filepath.Join(filepath.Dir(root), "oro-input")
	docker := filepath.Join(filepath.Dir(root), "docker-input")
	for _, path := range []string{oro, docker} {
		if err := os.WriteFile(path, []byte("program"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	socketDir, err := os.MkdirTemp("/tmp", "oro-publish-")
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

	// Kill a real publisher after its durable intent and sole Oro stage appear.
	if err := os.Chmod(oro, 0o700); err != nil {
		t.Fatal(err)
	}
	large := make([]byte, 64<<20)
	if err := os.WriteFile(oro, large, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oro, 0o500); err != nil {
		t.Fatal(err)
	}
	inputs.OroSHA256 = factoryHostDigest(large)

	if err := os.Chmod(oro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(oro, (512<<20)+1); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oro, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, spec.ID, ".runtime-publication.stage"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := publishFactoryRuntime(t.Context(), view, inputs); err == nil {
		t.Fatal("oversize oro accepted")
	}
	if err := os.Chmod(oro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(oro, int64(len(large))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oro, 0o500); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		View   factory.ManagedAdmissionView
		Inputs factoryRuntimeInputs
	}{view, inputs})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestFactoryRuntimePublication$")
	child.Env = append(os.Environ(), "ORO_PUBLICATION_CHILD="+string(payload))
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	stagePath := filepath.Join(root, spec.ID, ".oro.stage")
	limit := time.Now().Add(10 * time.Second)
	observed := false
	for time.Now().Before(limit) {
		if _, err := os.Lstat(stagePath); err == nil {
			observed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !observed {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatal("publisher never reached Oro staging")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := child.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("publisher did not die by signal: %v", waitErr)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("publisher exit was not SIGKILL: %v", waitErr)
	}
	if _, err := os.Lstat(stagePath); err != nil {
		t.Fatalf("killed publisher did not retain stage: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, spec.ID, "oro")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("killed publisher already finalized Oro: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.ID, "runtime-publication.json")); err != nil {
		t.Fatalf("intent not durable before stage: %v", err)
	}
	var refs [2]factory.ManagedRuntimeReference
	var errs [2]error
	var wg sync.WaitGroup
	for i := range refs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); refs[i], errs[i] = publishFactoryRuntime(t.Context(), view, inputs) }(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || refs[0] != refs[1] {
		t.Fatalf("concurrent publication = %v %v", refs, errs)
	}
	ref := refs[0]
	if err := verifyFactoryPublishedRuntime(t.Context(), view, ref); err != nil {
		t.Fatal(err)
	}

	objective := filepath.Join(root, spec.ID)
	if err := os.Rename(filepath.Join(objective, "runtime"), filepath.Join(objective, ".runtime.stage")); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(objective, ".runtime.stage", "unexpected")
	if err := os.WriteFile(extra, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := publishFactoryRuntime(t.Context(), view, inputs); err == nil {
		t.Fatal("hostile stage accepted")
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	recovered, err := publishFactoryRuntime(t.Context(), view, inputs)
	if err != nil || recovered != ref {
		t.Fatalf("recovery = %+v, %v", recovered, err)
	}

	link := filepath.Join(objective, "oro-hardlink")
	if err := os.Link(filepath.Join(objective, "oro"), link); err != nil {
		t.Fatal(err)
	}
	if err := verifyFactoryPublishedRuntime(t.Context(), view, ref); err == nil {
		t.Fatal("final oro with hard link accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oro); err != nil {
		t.Fatal(err)
	}
	again, err := publishFactoryRuntime(t.Context(), view, inputs)
	if err != nil || again != ref {
		t.Fatalf("replay = %+v, %v", again, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := publishFactoryRuntime(canceled, view, inputs); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	stage := filepath.Join(objective, ".oro.stage")
	if err := os.Symlink(filepath.Join(objective, "oro"), stage); err != nil {
		t.Fatal(err)
	}
	if _, err := publishFactoryRuntime(t.Context(), view, inputs); err == nil {
		t.Fatal("aliased oro stage accepted")
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(objective, "oro"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := publishFactoryRuntime(t.Context(), view, inputs); err == nil {
		t.Fatal("writable final oro accepted")
	}
	if err := os.Chmod(filepath.Join(objective, "oro"), 0o500); err != nil {
		t.Fatal(err)
	}

	if _, err := factory.ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
		t.Fatal("publication activated admission")
	}
	for _, name := range []string{".oro.stage", ".runtime.stage", ".runtime-publication.stage"} {
		if _, err := os.Lstat(filepath.Join(objective, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected retained stage %s: %v", name, err)
		}
	}
	info, err := os.Lstat(filepath.Join(objective, "oro"))
	if err != nil || info.Mode().Perm() != 0o500 || info.Size() != int64(len(large)) {
		t.Fatalf("unexpected final oro: %v %v", info, err)
	}
	conflict := inputs
	conflict.OroSHA256 = factoryHostDigest([]byte("different"))
	if _, err := publishFactoryRuntime(t.Context(), view, conflict); err == nil {
		t.Fatal("conflicting intent accepted")
	}

	if err := os.WriteFile(oro, large, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := factory.ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	activeRef, err := factory.ReadManagedRuntimeReference(t.Context(), root, spec.ID)
	if err != nil || activeRef != ref {
		t.Fatalf("committed active reference = %+v, %v", activeRef, err)
	}
	before := publicationJournalState(t, objective)
	if err := os.Remove(oro); err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(objective, "runtime-publication.json")
	if err := os.Remove(intentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := publishFactoryRuntime(t.Context(), view, inputs); err == nil {
		t.Fatal("adopted artifacts without intent")
	}
	if err := verifyFactoryPublishedRuntime(t.Context(), view, activeRef); err != nil {
		t.Fatalf("historical verification depended on intent: %v", err)
	}
	after := publicationJournalState(t, objective)
	if before != after {
		t.Fatalf("active verification changed admission, activation, snapshot, supervision or events: before %v after %v", before, after)
	}
	reread, err := factory.ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil || reread != view {
		t.Fatalf("admission changed: %+v %v", reread, err)
	}
	reref, err := factory.ReadManagedRuntimeReference(t.Context(), root, spec.ID)
	if err != nil || reref != activeRef {
		t.Fatalf("active reference changed: %+v %v", reref, err)
	}
	if err := os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	if err := verifyFactoryPublishedRuntime(t.Context(), view, ref); err == nil {
		t.Fatal("missing active runtime accepted")
	}
	if _, err := os.Lstat(ref.Path); !os.IsNotExist(err) {
		t.Fatalf("verifier recreated runtime: %v", err)
	}
	lockPath := filepath.Join(objective, "runtime-publication.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := verifyFactoryPublishedRuntime(t.Context(), view, ref); err == nil {
		t.Fatal("missing lock accepted")
	}
	if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("verifier recreated lock: %v", err)
	}
}

func publicationJournalState(t *testing.T, objective string) [6]string {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(objective, "state.db"), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries := []string{
		"SELECT hex(body) FROM managed_admissions WHERE id=1",
		"SELECT hex(body) FROM managed_activation WHERE id=1",
		"SELECT printf('%d:%s',revision,hex(body)) FROM snapshots WHERE id=1",
		"SELECT printf('%d:%s',revision,hex(body)) FROM managed_supervision WHERE id=1",
		"SELECT coalesce(group_concat(row_value,'|'),'') FROM (SELECT printf('%d:%s:%s',revision,state,hex(body)) AS row_value FROM events ORDER BY revision)",
		"SELECT coalesce(group_concat(row_value,'|'),'') FROM (SELECT printf('%d:%s:%s:%s',revision,kind,ref,hex(body)) AS row_value FROM managed_events ORDER BY revision)",
	}
	var result [6]string
	for i, query := range queries {
		if err := db.QueryRowContext(t.Context(), query).Scan(&result[i]); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
