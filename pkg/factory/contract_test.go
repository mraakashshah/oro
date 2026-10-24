package factory //nolint:testpackage // Admission and policy invariants use private package seams.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func contractSpec() Spec {
	return Spec{ID: "calculator", Objective: "Implement addition", Repository: "/tmp/repository", TargetBranch: "factory/calculator",
		Worker: Command{Path: "/tmp/worker"}, Acceptance: Command{Path: "/tmp/acceptance"}, Review: Command{Path: "/tmp/review"},
		RequiredChecks: []string{"positive", "negative"}, MaxAttempts: 3, TimeoutSeconds: 300, StepTimeoutSeconds: 60}
}

func policyFixture(t *testing.T) (string, Spec) {
	t.Helper()
	root := t.TempDir()
	spec := contractSpec()
	spec.Repository = filepath.Join(root, "repository")
	if err := os.Mkdir(spec.Repository, 0o700); err != nil {
		t.Fatal(err)
	}
	commands := []*Command{&spec.Worker, &spec.Acceptance, &spec.Review}
	for i, stage := range []string{"worker", "acceptance", "review"} {
		commands[i].Path = filepath.Join(root, stage)
		if err := os.WriteFile(commands[i].Path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "new", "state"), spec
}

func TestImmutablePolicies(t *testing.T) {
	state, spec := policyFixture(t)
	spec.Worker.Args = []string{"literal argument"}
	p, err := loadPolicies(state, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Spec, spec) {
		t.Fatal("admission changed logical spec")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("policy loading wrote state")
	}
	again, err := loadPolicies(state, spec)
	if err != nil || p.Hash != again.Hash {
		t.Fatalf("unstable identity: %v", err)
	}
	if err := freezePolicies(p); err != nil {
		t.Fatal(err)
	}
	if err := verifyPolicies(p); err != nil {
		t.Fatal(err)
	}
	if err := freezePolicies(p); err != nil {
		t.Fatal(err)
	}
	cmd := p.command("worker")
	if cmd.Path != filepath.Join(p.Dir, "worker") || !reflect.DeepEqual(cmd.Args, spec.Worker.Args) {
		t.Fatalf("frozen command = %#v", cmd)
	}
	info, err := os.Stat(cmd.Path)
	if err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("frozen permissions: %v, %v", info, err)
	}
	spec.Objective = "Changed objective"
	changed, err := loadPolicies(state, spec)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Hash == p.Hash {
		t.Fatal("changed spec retained policy identity")
	}
	if err := freezePolicies(changed); err == nil {
		t.Fatal("changed admission overwrote frozen identity")
	}
	if err := os.Chmod(cmd.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmd.Path, []byte("tampered"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := verifyPolicies(p); err == nil {
		t.Fatal("tampering accepted")
	}
	if err := freezePolicies(p); err == nil {
		t.Fatal("tampered policy silently replaced")
	}
	bytes, err := os.ReadFile(cmd.Path)
	if err != nil || string(bytes) != "tampered" {
		t.Fatal("tampering overwritten")
	}
	if err := os.WriteFile(spec.Review.Path, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	updated, err := loadPolicies(state, spec)
	if err != nil || updated.Hash == changed.Hash {
		t.Fatalf("program bytes not bound: %v", err)
	}
}

func TestPolicyPathAdmission(t *testing.T) {
	cases := []string{"contained state", "contained program", "symlink state", "symlink objective", "symlink program", "missing program", "nonexecutable", "directory program", "relative state", "missing repository"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			state, spec := policyFixture(t)
			switch name {
			case "contained state":
				state = filepath.Join(spec.Repository, "missing", "state")
			case "contained program":
				spec.Worker.Path = filepath.Join(spec.Repository, "policy")
				if err := os.WriteFile(spec.Worker.Path, []byte("x"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink state":
				link := filepath.Join(filepath.Dir(spec.Repository), "link")
				if err := os.Symlink(spec.Repository, link); err != nil {
					t.Fatal(err)
				}
				state = filepath.Join(link, "missing", "state")
			case "symlink program":
				inside := filepath.Join(spec.Repository, "policy")
				if err := os.WriteFile(inside, []byte("x"), 0o700); err != nil {
					t.Fatal(err)
				}
				spec.Worker.Path = filepath.Join(filepath.Dir(spec.Repository), "linked-policy")
				if err := os.Symlink(inside, spec.Worker.Path); err != nil {
					t.Fatal(err)
				}
			case "symlink objective":
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(spec.Repository, filepath.Join(state, spec.ID)); err != nil {
					t.Fatal(err)
				}
			case "missing program":
				spec.Worker.Path += "missing"
			case "nonexecutable":
				if err := os.Chmod(spec.Worker.Path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory program":
				spec.Worker.Path = filepath.Dir(spec.Repository)
			case "relative state":
				state = "state"
			case "missing repository":
				spec.Repository += "missing"
			}
			if _, err := loadPolicies(state, spec); err == nil {
				t.Fatal("unsafe policy path accepted")
			}
		})
	}
}

func TestAdmissionContract(t *testing.T) {
	if err := validateSpec(contractSpec()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Spec){
		"empty objective":       func(s *Spec) { s.Objective = "  " },
		"empty ID":              func(s *Spec) { s.ID = "" },
		"invalid ID":            func(s *Spec) { s.ID = "../other" },
		"long ID":               func(s *Spec) { s.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"relative repository":   func(s *Spec) { s.Repository = "repo" },
		"empty target":          func(s *Spec) { s.TargetBranch = "" },
		"relative worker":       func(s *Spec) { s.Worker.Path = "worker" },
		"relative acceptance":   func(s *Spec) { s.Acceptance.Path = "acceptance" },
		"relative review":       func(s *Spec) { s.Review.Path = "review" },
		"no checks":             func(s *Spec) { s.RequiredChecks = nil },
		"empty check":           func(s *Spec) { s.RequiredChecks = []string{" "} },
		"duplicate checks":      func(s *Spec) { s.RequiredChecks = []string{"positive", "positive"} },
		"zero attempts":         func(s *Spec) { s.MaxAttempts = 0 },
		"negative attempts":     func(s *Spec) { s.MaxAttempts = -1 },
		"zero deadline":         func(s *Spec) { s.TimeoutSeconds = 0 },
		"zero step":             func(s *Spec) { s.StepTimeoutSeconds = 0 },
		"step exceeds deadline": func(s *Spec) { s.StepTimeoutSeconds = 301 },
		"duration overflow":     func(s *Spec) { s.TimeoutSeconds = int(^uint(0) >> 1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			spec := contractSpec()
			change(&spec)
			if err := validateSpec(spec); err == nil {
				t.Fatal("invalid specification accepted")
			}
		})
	}
}
