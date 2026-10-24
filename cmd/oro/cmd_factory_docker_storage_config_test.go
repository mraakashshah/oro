package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryDockerStorageConfig(t *testing.T) {
	t.Run("exact immutable producer view and read only behavior", testFactoryDockerStorageConfigExact)
	t.Run("every represented admission field must match", testFactoryDockerStorageConfigMismatch)
	t.Run("each callback reopens and verifies frozen files", testFactoryDockerStorageConfigReread)
	t.Run("cancellation and missing inputs never initialize", testFactoryDockerStorageConfigFailure)
}

// This is a trusted-view fixture, not proof of a committed managed admission.
// Production obtains the immutable view through factory.ReadManagedAdmission.
func factoryDockerStorageView(config factoryRuntimeConfig) factory.ManagedAdmissionView {
	return factory.ManagedAdmissionView{
		StateRoot: config.StateRoot, ObjectiveID: config.ObjectiveID, PolicyHash: config.PolicyHash,
		GitPath: config.GitPath, GitSHA256: config.GitSHA256, Repository: config.Repository, TargetRef: config.TargetRef,
		Deadline: config.Deadline, HardStop: config.HardStop, CleanupTimeoutSeconds: config.CleanupTimeoutSeconds, DockerContext: config.DockerContext, ImageDigest: "sha256:" + strings.Repeat("b", 64),
	}
}

func testFactoryDockerStorageConfigExact(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	view := factoryDockerStorageView(f.config)
	before := factoryRuntimeTree(t, f.config.StateRoot)
	reader := newFactoryDockerConfigReader(view, f.path, factoryHostDigest(f.data))
	t.Setenv("DOCKER_HOST", "tcp://untrusted:2375")
	t.Setenv("DOCKER_CONTEXT", "default")
	t.Setenv("DOCKER_CONFIG", "/missing")
	expected := factory.ManagedDockerConfig{
		StateRoot: f.config.StateRoot, ObjectiveID: f.config.ObjectiveID, PolicyHash: f.config.PolicyHash,
		Deadline: f.config.Deadline, HardStop: f.config.HardStop, RuntimeConfigPath: f.path, RuntimeConfigSHA256: factoryHostDigest(f.data),
		OroPath: f.config.OroPath, OroSHA256: f.config.OroSHA256,
		DockerPath: f.config.DockerPath, DockerSHA256: f.config.DockerSHA256, DockerConfig: filepath.Join(filepath.Dir(f.path), "docker"), DockerContext: f.config.DockerContext, DockerEndpoint: f.config.DockerEndpoint,
	}
	// Capture value semantics: later caller mutations cannot rewrite the callback.
	view.PolicyHash = strings.Repeat("c", 64)
	for range 2 {
		got, err := reader(t.Context())
		if err != nil || got != expected {
			t.Fatalf("verified DTO %+v %v", got, err)
		}
	}
	if !f.config.Deadline.Before(time.Now()) {
		t.Fatal("fixture must exercise expired work identity")
	}
	if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(after, before) {
		t.Fatal("reader initialized or changed state/artifacts")
	}
}

func testFactoryDockerStorageConfigMismatch(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	for name, mutate := range map[string]func(*factory.ManagedAdmissionView){
		"root":              func(v *factory.ManagedAdmissionView) { v.StateRoot += "-other" },
		"objective":         func(v *factory.ManagedAdmissionView) { v.ObjectiveID = "other" },
		"policy":            func(v *factory.ManagedAdmissionView) { v.PolicyHash = strings.Repeat("c", 64) },
		"git path":          func(v *factory.ManagedAdmissionView) { v.GitPath += "-other" },
		"git hash":          func(v *factory.ManagedAdmissionView) { v.GitSHA256 = strings.Repeat("c", 64) },
		"repository":        func(v *factory.ManagedAdmissionView) { v.Repository += "-other" },
		"target ref":        func(v *factory.ManagedAdmissionView) { v.TargetRef = "refs/heads/other" },
		"deadline":          func(v *factory.ManagedAdmissionView) { v.Deadline = v.Deadline.Add(time.Second) },
		"hard stop":         func(v *factory.ManagedAdmissionView) { v.HardStop = v.HardStop.Add(time.Second) },
		"cleanup allowance": func(v *factory.ManagedAdmissionView) { v.CleanupTimeoutSeconds++ },
		"context":           func(v *factory.ManagedAdmissionView) { v.DockerContext = "other-context" },
	} {
		t.Run(name, func(t *testing.T) {
			view := factoryDockerStorageView(f.config)
			mutate(&view)
			before := factoryRuntimeTree(t, f.config.StateRoot)
			reader := newFactoryDockerConfigReader(view, f.path, factoryHostDigest(f.data))
			if got, err := reader(t.Context()); err == nil || got != (factory.ManagedDockerConfig{}) {
				t.Fatalf("mismatched view returned configuration %+v %v", got, err)
			}
			if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(after, before) {
				t.Fatal("identity failure changed runtime state")
			}
		})
	}
}

func testFactoryDockerStorageConfigReread(t *testing.T) {
	for _, mutation := range []string{"artifact bytes", "wrong oro", "wrong git", "wrong docker", "oro program bytes", "git program bytes", "docker program bytes", "meta endpoint", "cli auth", "endpoint regular", "config symlink", "meta hardlink"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFactoryRuntimeConfigFixture(t)
			var program string
			if strings.HasSuffix(mutation, " program bytes") {
				program = factoryDockerStorageUniqueProgram(t, &f, strings.TrimSuffix(mutation, " program bytes"))
			}
			reader := newFactoryDockerConfigReader(factoryDockerStorageView(f.config), f.path, factoryHostDigest(f.data))
			if _, err := reader(t.Context()); err != nil {
				t.Fatal(err)
			}
			switch {
			case mutation == "artifact bytes":
				factoryRuntimeConfigWrite(t, f.path, append(append([]byte(nil), f.data...), '\n'))
			case program != "":
				if err := os.Chmod(program, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 74\n"), 0o500); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(program, 0o500); err != nil {
					t.Fatal(err)
				}
			default:
				mutateFactoryRuntimeConfigFixture(t, &f, mutation)
			}
			before := factoryRuntimeTree(t, f.config.StateRoot)
			if got, err := reader(t.Context()); err == nil || got != (factory.ManagedDockerConfig{}) {
				t.Fatalf("changed %s was cached: %+v %v", mutation, got, err)
			}
			if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(after, before) {
				t.Fatal("failed verification repaired or rewrote state")
			}
		})
	}
}

func factoryDockerStorageUniqueProgram(t *testing.T, f *factoryRuntimeConfigFixture, kind string) string {
	t.Helper()
	path := filepath.Join(f.config.StateRoot, kind+"-program")
	data, err := os.ReadFile(f.config.DockerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o500); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "oro":
		f.config.OroPath = path
	case "git":
		f.config.GitPath = path
	case "docker":
		f.config.DockerPath = path
	default:
		t.Fatal("unknown program kind")
	}
	f.data, err = json.Marshal(f.config)
	if err != nil {
		t.Fatal(err)
	}
	factoryRuntimeConfigWrite(t, f.path, f.data)
	return path
}

func testFactoryDockerStorageConfigFailure(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	view := factoryDockerStorageView(f.config)
	before := factoryRuntimeTree(t, f.config.StateRoot)
	reader := newFactoryDockerConfigReader(view, f.path, factoryHostDigest(f.data))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := reader(ctx); !errors.Is(err, context.Canceled) || got != (factory.ManagedDockerConfig{}) {
		t.Fatalf("cancellation %+v %v", got, err)
	}
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	if got, err := reader(expired); !errors.Is(err, context.DeadlineExceeded) || got != (factory.ManagedDockerConfig{}) {
		t.Fatalf("expired context %+v %v", got, err)
	}
	for _, reference := range []struct{ path, digest string }{{filepath.Join(f.config.StateRoot, "missing", "runtime", "runtime.json"), factoryHostDigest(f.data)}, {f.path, "invalid"}, {f.path, strings.Repeat("0", 64)}} {
		read := newFactoryDockerConfigReader(view, reference.path, reference.digest)
		if got, err := read(t.Context()); err == nil || got != (factory.ManagedDockerConfig{}) {
			t.Fatalf("invalid reference %+v %v", got, err)
		}
	}
	if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(after, before) {
		t.Fatal("failure initialized state or artifacts")
	}
}
