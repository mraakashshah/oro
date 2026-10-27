package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
	"oro/pkg/storage"
)

func TestFactoryTerraEntry(t *testing.T) {
	f := newFactoryStartFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.oro, f.model} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, binary, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	credential := filepath.Join(filepath.Dir(f.root), "missing-credential-directory", "absent-credential")
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls++; return nil }}
	run := func(credential string) error {
		root := newRootCmd()
		family, _, err := root.Find([]string{"factory"})
		if err != nil {
			t.Fatal(err)
		}
		old, _, err := family.Find([]string{"start"})
		if err != nil {
			t.Fatal(err)
		}
		family.RemoveCommand(old)
		family.AddCommand(buildFactoryStartCmd(deps))
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", credential})
		return root.ExecuteContext(t.Context())
	}
	if err := run(credential); err != nil {
		t.Fatal(err)
	}
	ref, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := readFactoryRuntimeConfig(t.Context(), ref.Path, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readFactoryRuntimeConfig(t.Context(), ref.Path, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if first.ModelHelper == nil || first.ModelHelper == second.ModelHelper || !factory.EqualModelAdapterConfig(first.ModelHelper, second.ModelHelper) || first.ModelHelper.CredentialPath != credential || first.ModelHelper.HelperSHA256 != first.OroSHA256 {
		t.Fatal("runtime did not preserve helper identity by value")
	}
	if err := run(credential); err != nil || calls != 1 {
		t.Fatalf("replay failed: %v, calls %d", err, calls)
	}
	if err := run(credential + "-changed"); err == nil || calls != 1 {
		t.Fatal("changed credential reference accepted")
	}
	after, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID)
	if err != nil || after != ref {
		t.Fatal("runtime changed on replay")
	}
	helper, _, err := newFactoryCmd().Find([]string{"_model-terra"})
	if err != nil || helper.Name() != "_model-terra" {
		t.Fatal("helper command missing")
	}
	data, err := json.Marshal(first.factoryRuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	prefix := data[:bytes.Index(data, []byte(`,"model_helper":`))]
	for _, malformed := range [][]byte{
		append(append([]byte{}, prefix...), []byte(`,"model_helper":null}`)...),
		append(append([]byte{}, prefix...), []byte(`,"model_helper":{}}`)...),
		bytes.Replace(data, []byte(`"model_helper"`), []byte(`"Model_Helper"`), 1),
		bytes.Replace(data, []byte(`"protocol":"oro-terra-responses-v1"`), []byte(`"protocol":""`), 1),
		bytes.Replace(data, []byte(`"protocol"`), []byte(`"Protocol"`), 1),
		bytes.Replace(data, []byte(`"protocol":`), []byte(`"protocol":"oro-terra-responses-v1","protocol":`), 1),
	} {
		if _, err := decodeFactoryRuntimeConfig(malformed); err == nil {
			t.Fatal("malformed helper accepted")
		}
	}
	old := first.factoryRuntimeConfig
	old.ModelHelper = nil
	legacy, err := canonicalFactoryRuntimeConfig(old)
	if err != nil || strings.Contains(string(legacy), "model_helper") {
		t.Fatal("historical config changed")
	}
	if _, err := decodeFactoryRuntimeConfig(legacy); err != nil {
		t.Fatal(err)
	}
	wire := testFactoryModelWire(t, ref, *first.ModelHelper, f.root, f.spec.ID, first.Deadline)
	testFactoryModelCommand(t, ref, wire)
	testFactoryUnqualifiedModelChild(t, f.root, f.spec.ID, ref, wire)
	t.Run("different admitted model", testFactoryDifferentModel)
	t.Run("repository alias rejects before allocation", testFactoryCredentialRepositoryAlias)
	t.Run("state alias rejects before allocation", testFactoryCredentialStateAlias)
	t.Run("dangling alias rejects before allocation", testFactoryCredentialDanglingAlias)
	t.Run("case alias rejects before allocation", testFactoryCredentialCaseAlias)
	t.Run("state case alias rejects before allocation", testFactoryCredentialStateCaseAlias)
	t.Run("missing state case alias rejects before allocation", testFactoryCredentialMissingStateCaseAlias)
	t.Run("distinct case directories remain allowed", testFactoryDistinctCaseDirectories)
	t.Run("prospective name policy", testFactoryProspectiveNamePolicy)
}

func testFactoryModelWire(t *testing.T, ref factory.ManagedRuntimeReference, helper factory.ModelAdapterConfig, root, id string, deadline time.Time) []byte {
	t.Helper()
	host := factory.ScopeHandle{ID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 32), Kind: "host", Backend: "host", Name: "model", Generation: 1}
	candidate := factory.ScopeHandle{ID: strings.Repeat("c", 32), Nonce: strings.Repeat("d", 32), Kind: "container", Backend: "docker", Name: "candidate", Generation: 1}
	request := factory.ModelRequest{Version: 1, StateRoot: root, ObjectiveID: id, Invocation: host, CandidateID: strings.Repeat("e", 32), CandidateScope: candidate, CandidateBindingSHA256: strings.Repeat("f", 64), Runtime: ref, Adapter: helper, Role: "worker", Model: "gpt-6.1-terra", ExecutionDeadline: deadline, MaxTurns: 1, InvocationLiabilityCents: 1, InputTokenCeiling: 16384, OutputTokenCeiling: 4096, TranscriptByteLimit: 1024, ToolSocket: "/private/tmp/model.sock", Prompt: "objective"}
	encoded, err := factory.EncodeModelRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.DecodeModelRequest(encoded); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(encoded, []byte(`"version":1`), []byte(`"Version":1`), 1),
		bytes.Replace(encoded, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(encoded, []byte(`"prompt":"objective"`), []byte(`"prompt":null`), 1),
		append(append([]byte{}, encoded...), ' '),
	} {
		if _, err := factory.DecodeModelRequest(invalid); err == nil {
			t.Fatal("noncanonical model request accepted")
		}
	}
	return encoded
}

func testFactoryModelCommand(t *testing.T, ref factory.ManagedRuntimeReference, wire []byte) {
	t.Helper()
	request, err := factory.DecodeModelRequest(wire)
	if err != nil {
		t.Fatal(err)
	}
	request.ExecutionDeadline = time.Now().UTC().Add(-time.Second)
	expired, err := factory.EncodeModelRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExecutionDeadline = time.Now().UTC().Add(time.Minute)
	request.Adapter.CredentialPath += "-other"
	different, err := factory.EncodeModelRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		input    []byte
		expected string
	}{
		"wrong reference":   {bytes.Replace(wire, []byte(ref.SHA256), []byte(strings.Repeat("f", 64)), 1), "model request differs from frozen runtime"},
		"malformed":         {append(append([]byte{}, wire...), ' '), "model request is not canonical"},
		"expired":           {expired, "model request differs from frozen runtime"},
		"different adapter": {different, "model request differs from frozen runtime"},
		"wrong executable":  {wire, "model helper is not the admitted executable"},
	} {
		t.Run(name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := writer.Write(tc.input); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			root := newRootCmd()
			root.SetIn(reader)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"factory", "_model-terra", "--runtime-config", ref.Path, "--runtime-config-sha256", ref.SHA256})
			if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("unexpected helper validation result: %v", err)
			}
		})
	}
}

func TestFactoryTerraEntryChild(t *testing.T) {
	if os.Getenv("TEST_FACTORY_TERRA_CHILD") != "1" {
		return
	}
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "_model-terra", "--runtime-config", os.Getenv("TEST_FACTORY_TERRA_PATH"), "--runtime-config-sha256", os.Getenv("TEST_FACTORY_TERRA_DIGEST")})
	err := root.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "terra model adapter unavailable") {
		t.Fatalf("unexpected helper result: %v", err)
	}
}

func testFactoryUnqualifiedModelChild(t *testing.T, root, id string, ref factory.ManagedRuntimeReference, input []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, filepath.Join(root, id, "model"), "-test.run=^TestFactoryTerraEntryChild$", "-test.count=1", "-test.timeout=20s")
	if coverage := flag.Lookup("test.gocoverdir"); coverage != nil && coverage.Value.String() != "" {
		child.Args = append(child.Args, "-test.gocoverdir="+coverage.Value.String())
	}
	child.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C", "TEST_FACTORY_TERRA_CHILD=1", "TEST_FACTORY_TERRA_PATH=" + ref.Path, "TEST_FACTORY_TERRA_DIGEST=" + ref.SHA256}
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	child.Cancel = func() error { return syscall.Kill(-child.Process.Pid, syscall.SIGKILL) }
	child.WaitDelay = 3 * time.Second
	t.Cleanup(func() {
		if child.Process != nil && child.ProcessState == nil {
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			_ = child.Wait()
		}
	})
	child.Stdin = bytes.NewReader(input)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("unqualified actual helper: %s: %v", output, err)
	}
}

func testFactoryDifferentModel(t *testing.T) {
	f := newFactoryStartFixture(t)
	if err := os.Chmod(f.model, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.model, []byte("different model bytes"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.model, 0o500); err != nil {
		t.Fatal(err)
	}
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { return nil }}
	credential := filepath.Join(filepath.Dir(f.root), "unused-credential")
	if _, err := startFactoryManaged(t.Context(), f.root, f.spec, f.docker, f.endpoint, deps, credential); err != nil {
		t.Fatal(err)
	}
	ref, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err := readFactoryRuntimeConfig(t.Context(), ref.Path, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	data := testFactoryModelWire(t, ref, *config.ModelHelper, f.root, f.spec.ID, config.Deadline)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd()
	command.SetIn(reader)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"factory", "_model-terra", "--runtime-config", ref.Path, "--runtime-config-sha256", ref.SHA256})
	if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "model helper differs from admitted program") {
		t.Fatalf("different executable accepted or failed early: %v", err)
	}
}

func testFactoryCredentialRepositoryAlias(t *testing.T) {
	f := newFactoryStartFixture(t)
	canonical := f.spec.Repository
	alias := filepath.Join(filepath.Dir(f.root), "repo-alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	f.spec.Repository = alias
	spec, err := json.Marshal(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.specPath, spec, 0o600); err != nil {
		t.Fatal(err)
	}
	probes := 0
	deps := factoryStartDependencies{probe: func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probes++
		return factoryBudgetProbe(math.MaxInt64)(ctx, path)
	}, executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { t.Fatal("invalid credential reached bootstrap"); return nil }}
	root := newRootCmd()
	family, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := family.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(old)
	family.AddCommand(buildFactoryStartCmd(deps))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", filepath.Join(canonical, "credential")})
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("repository-contained credential accepted")
	}
	if probes != 0 {
		t.Fatalf("invalid credential reached allocation probe %d times", probes)
	}
}

func testFactoryCredentialStateAlias(t *testing.T) {
	f := newFactoryStartFixture(t)
	if err := os.Mkdir(f.root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(f.root), "state-alias")
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	probes := 0
	deps := factoryStartDependencies{probe: func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probes++
		return factoryBudgetProbe(math.MaxInt64)(ctx, path)
	}, executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { t.Fatal("invalid credential reached bootstrap"); return nil }}
	root := newRootCmd()
	family, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := family.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(old)
	family.AddCommand(buildFactoryStartCmd(deps))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", alias, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", filepath.Join(f.root, "credential")})
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("state-contained credential accepted")
	}
	if probes != 0 {
		t.Fatalf("invalid credential reached allocation probe %d times", probes)
	}
}

func testFactoryCredentialDanglingAlias(t *testing.T) {
	f := newFactoryStartFixture(t)
	alias := filepath.Join(filepath.Dir(f.root), "dangling-credential-parent")
	if err := os.Symlink(filepath.Join(f.spec.Repository, "not-created"), alias); err != nil {
		t.Fatal(err)
	}
	probes := 0
	deps := factoryStartDependencies{probe: func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probes++
		return factoryBudgetProbe(math.MaxInt64)(ctx, path)
	}, executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { t.Fatal("dangling credential reached bootstrap"); return nil }}
	root := newRootCmd()
	family, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := family.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(old)
	family.AddCommand(buildFactoryStartCmd(deps))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", filepath.Join(alias, "key")})
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("dangling credential parent accepted")
	}
	if probes != 0 {
		t.Fatalf("dangling credential reached allocation probe %d times", probes)
	}
}

func testFactoryCredentialCaseAlias(t *testing.T) {
	f := newFactoryStartFixture(t)
	repo := filepath.Join(filepath.Dir(f.spec.Repository), "Repo")
	if err := os.Rename(f.spec.Repository, repo); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(repo), "rEPO")
	realInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem distinguishes case")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(realInfo, aliasInfo) {
		t.Skip("case variant names a different directory")
	}
	f.spec.Repository = repo
	data, err := json.Marshal(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	probes := 0
	deps := factoryStartDependencies{probe: func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probes++
		return factoryBudgetProbe(math.MaxInt64)(ctx, path)
	}, executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { return errors.New("unexpected bootstrap") }}
	root := newRootCmd()
	family, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := family.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(old)
	family.AddCommand(buildFactoryStartCmd(deps))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", filepath.Join(alias, "missing", "credential")})
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("case alias credential accepted")
	}
	if probes != 0 {
		t.Fatalf("case alias reached allocation probe %d times", probes)
	}
}

func testFactoryCredentialStateCaseAlias(t *testing.T) {
	f := newFactoryStartFixture(t)
	if err := os.Mkdir(f.root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(f.root), strings.ToUpper(filepath.Base(f.root)))
	realInfo, err := os.Stat(f.root)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem distinguishes case")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(realInfo, aliasInfo) {
		t.Skip("case variant names a different directory")
	}
	probes := 0
	deps := factoryStartDependencies{probe: func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probes++
		return factoryBudgetProbe(math.MaxInt64)(ctx, path)
	}, executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { return errors.New("unexpected bootstrap") }}
	root := newRootCmd()
	family, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := family.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(old)
	family.AddCommand(buildFactoryStartCmd(deps))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", filepath.Join(alias, "missing", "credential")})
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("state case alias credential accepted")
	}
	if probes != 0 {
		t.Fatalf("state case alias reached allocation probe %d times", probes)
	}
}

func testFactoryDistinctCaseDirectories(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(base, "Repo"), filepath.Join(base, "rEPO")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); errors.Is(err, os.ErrExist) {
		t.Skip("filesystem folds case")
	} else if err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(a, b) {
		t.Fatal("case-distinct directories have identical identity")
	}
	if err := rejectCredentialPhysicalContainment(second, first); err != nil {
		t.Fatalf("distinct directories rejected: %v", err)
	}
}

func testFactoryCredentialMissingStateCaseAlias(t *testing.T) {
	f := newFactoryStartFixture(t)
	alias := filepath.Join(filepath.Dir(f.root), strings.ToUpper(filepath.Base(f.root)))
	probeName := filepath.Join(filepath.Dir(f.root), "CaseProbe")
	if err := os.Mkdir(probeName, 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(probeName)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(filepath.Dir(probeName), "cASEpROBE"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem distinguishes case")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Skip("case variant names a different directory")
	}
	probes := 0
	deps := factoryStartDependencies{probe: func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probes++
		return factoryBudgetProbe(math.MaxInt64)(ctx, path)
	}, executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { return errors.New("unexpected bootstrap") }}
	root := newRootCmd()
	family, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := family.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(old)
	family.AddCommand(buildFactoryStartCmd(deps))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint, "--model-credential-file", filepath.Join(alias, "missing", "credential")})
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("missing state case alias credential accepted")
	}
	if probes != 0 {
		t.Fatalf("missing state case alias reached allocation probe %d times", probes)
	}
}

func testFactoryProspectiveNamePolicy(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := compareProspectiveCredentialName(dir, "other", "state"); err != nil {
		t.Fatalf("unrelated ASCII name rejected: %v", err)
	}
	if err := compareProspectiveCredentialName(dir, "ſtate", "state"); err == nil {
		t.Fatal("ambiguous Unicode name accepted")
	}
	if err := compareProspectiveCredentialName(dir, "state", "state"); err == nil {
		t.Fatal("same name accepted")
	}
	file, err := dir.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	sensitive, known := factoryCredentialCaseSensitive(file)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	err = compareProspectiveCredentialName(dir, "STATE", "state")
	if known && sensitive && err != nil {
		t.Fatalf("distinct names on case-sensitive filesystem rejected: %v", err)
	}
	if (!known || !sensitive) && err == nil {
		t.Fatal("ambiguous case alias accepted")
	}
}
