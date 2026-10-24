package main

import (
	"bytes"
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

func TestFactoryCleanupInvocation(t *testing.T) {
	t.Run("exact late cleanup is read only", testFactoryCleanupInvocationExact)
	t.Run("optional runtime reference shape", testFactoryCleanupReference)
	t.Run("exact helper command", testFactoryCleanupInvocationCommand)
	t.Run("authenticated permit bytes", testFactoryCleanupInvocationPermit)
	t.Run("trusted runtime identities", testFactoryCleanupInvocationConfig)
	t.Run("changed frozen artifacts", testFactoryCleanupInvocationArtifacts)
	t.Run("current immutable deadline", testFactoryCleanupInvocationDeadline)
}

type factoryCleanupInvocationFixture struct {
	runtime    factoryRuntimeConfigFixture
	config     factoryLaunchdConfig
	permit     factory.DockerCleanupPermit
	invocation factory.HostInvocation
}

func newFactoryCleanupInvocationFixture(t *testing.T) factoryCleanupInvocationFixture {
	t.Helper()
	f := newFactoryRuntimeConfigFixture(t)
	f.config.Deadline = time.Now().UTC().Add(-time.Minute)
	f.config.HardStop = f.config.Deadline.Add(3 * time.Minute)
	data, err := canonicalFactoryRuntimeConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.data = data
	factoryRuntimeConfigWrite(t, f.path, data)
	c := f.config
	config := factoryLaunchdConfig{
		StateRoot: c.StateRoot, ObjectiveID: c.ObjectiveID, OroPath: c.OroPath, OroSHA256: c.OroSHA256,
		GitPath: c.GitPath, GitSHA256: c.GitSHA256, Repository: c.Repository, TargetRef: c.TargetRef,
		CleanupTimeout: time.Duration(c.CleanupTimeoutSeconds) * time.Second, HardStop: c.HardStop,
		RuntimeConfigPath: f.path, RuntimeConfigSHA256: factoryHostDigest(data),
	}
	p := factoryDockerCleanupPlanFixture("volume")
	objective := factoryLaunchdObjectiveHash(config)
	p.Target.Name = "oro-factory-" + objective + "-volume-" + p.Target.ID + "-" + p.Target.Nonce
	p.Scope.Name = "dev.getoro.factory." + objective + ".cleanup." + p.Scope.ID + "." + p.Scope.Nonce
	p.Binding.Name, p.Binding.Context = p.Target.Name, c.DockerContext
	p.Binding.Labels["dev.getoro.factory.objective"] = objective
	p.Binding.Labels["dev.getoro.factory.policy"] = c.PolicyHash
	p.Deadline = time.Now().UTC().Add(time.Minute)
	invocation := factory.HostInvocation{
		Scope: p.Scope, Path: c.OroPath, Dir: c.StateRoot, Deadline: p.Deadline, OutputLimit: 1 << 20,
		Args: []string{"factory", "_docker-cleanup", "--runtime-config", f.path, "--runtime-config-sha256", config.RuntimeConfigSHA256},
		Env:  []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"},
	}
	fixture := factoryCleanupInvocationFixture{runtime: f, config: config, permit: p, invocation: invocation}
	fixture.encodePermit(t)
	return fixture
}

func (f *factoryCleanupInvocationFixture) encodePermit(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.permit)
	if err != nil {
		t.Fatal(err)
	}
	f.invocation.Stdin = data
}

func testFactoryCleanupInvocationExact(t *testing.T) {
	f := newFactoryCleanupInvocationFixture(t)
	before := factoryRuntimeTree(t, f.config.StateRoot)
	t.Setenv("DOCKER_HOST", "unix:///foreign")
	t.Setenv("DOCKER_CONTEXT", "default")
	t.Setenv("DOCKER_CONFIG", "/foreign")
	if err := validateFactoryLaunchdConfig(t.Context(), f.config); err != nil {
		t.Fatal(err)
	}
	if err := validateFactoryCleanupInvocation(t.Context(), f.config, f.invocation, f.permit); err != nil {
		t.Fatal(err)
	}
	if f.runtime.config.Deadline.After(time.Now()) {
		t.Fatal("fixture did not exercise cleanup after the work deadline")
	}
	if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("validation wrote admission, launch, or grant state")
	}
}

func testFactoryCleanupReference(t *testing.T) {
	f := newFactoryCleanupInvocationFixture(t)
	for _, present := range []bool{false, true} {
		c := f.config
		if !present {
			c.RuntimeConfigPath, c.RuntimeConfigSHA256 = "", ""
		}
		if err := validateFactoryLaunchdConfig(t.Context(), c); err != nil {
			t.Fatalf("optional reference present=%t: %v", present, err)
		}
		if !present && validateFactoryCleanupInvocation(t.Context(), c, f.invocation, f.permit) == nil {
			t.Fatal("cleanup accepted no frozen runtime reference")
		}
	}
	for name, mutate := range map[string]func(*factoryLaunchdConfig){
		"missing path":   func(c *factoryLaunchdConfig) { c.RuntimeConfigPath = "" },
		"missing digest": func(c *factoryLaunchdConfig) { c.RuntimeConfigSHA256 = "" },
		"digest length":  func(c *factoryLaunchdConfig) { c.RuntimeConfigSHA256 = "abc" },
		"digest case":    func(c *factoryLaunchdConfig) { c.RuntimeConfigSHA256 = strings.Repeat("A", 64) },
		"relative path":  func(c *factoryLaunchdConfig) { c.RuntimeConfigPath = "runtime/runtime.json" },
		"noncanonical path": func(c *factoryLaunchdConfig) {
			c.RuntimeConfigPath = filepath.Dir(c.RuntimeConfigPath) + "/../runtime/runtime.json"
		},
		"other objective": func(c *factoryLaunchdConfig) {
			c.RuntimeConfigPath = filepath.Join(c.StateRoot, "other", "runtime", "runtime.json")
		},
		"other root": func(c *factoryLaunchdConfig) { c.RuntimeConfigPath = "/runtime/runtime.json" },
	} {
		t.Run(name, func(t *testing.T) {
			c := f.config
			mutate(&c)
			if validateFactoryLaunchdConfig(t.Context(), c) == nil {
				t.Fatal("invalid runtime reference accepted by constructor")
			}
			if validateFactoryCleanupInvocation(t.Context(), c, f.invocation, f.permit) == nil {
				t.Fatal("invalid runtime reference accepted for cleanup")
			}
		})
	}
	// Constructors validate the optional reference's shape, not its contents.
	// Existing host and observer construction must not depend on Docker files.
	factoryRuntimeConfigWrite(t, f.runtime.path, []byte("changed"))
	if err := validateFactoryLaunchdConfig(t.Context(), f.config); err != nil {
		t.Fatalf("constructor read the runtime artifact: %v", err)
	}
}

func testFactoryCleanupInvocationCommand(t *testing.T) {
	f := newFactoryCleanupInvocationFixture(t)
	for name, mutate := range map[string]func(*factory.HostInvocation){
		"program":            func(i *factory.HostInvocation) { i.Path += "-other" },
		"command":            func(i *factory.HostInvocation) { i.Args[1] = "_host-exec" },
		"config path":        func(i *factory.HostInvocation) { i.Args[3] += "-other" },
		"config digest":      func(i *factory.HostInvocation) { i.Args[5] = strings.Repeat("f", 64) },
		"extra argument":     func(i *factory.HostInvocation) { i.Args = append(i.Args, "--help") },
		"missing argument":   func(i *factory.HostInvocation) { i.Args = i.Args[:5] },
		"argument order":     func(i *factory.HostInvocation) { i.Args[2], i.Args[4] = i.Args[4], i.Args[2] },
		"directory":          func(i *factory.HostInvocation) { i.Dir = filepath.Dir(i.Dir) },
		"ambient Docker":     func(i *factory.HostInvocation) { i.Env = append(i.Env, "DOCKER_HOST=unix:///foreign") },
		"home":               func(i *factory.HostInvocation) { i.Env = append(i.Env, "HOME=/foreign") },
		"environment value":  func(i *factory.HostInvocation) { i.Env[0] = "PATH=/foreign" },
		"environment order":  func(i *factory.HostInvocation) { i.Env[1], i.Env[2] = i.Env[2], i.Env[1] },
		"environment absent": func(i *factory.HostInvocation) { i.Env = nil },
		"output bound":       func(i *factory.HostInvocation) { i.OutputLimit++ },
		"scope ID":           func(i *factory.HostInvocation) { i.Scope.ID = strings.Repeat("f", 32) },
		"scope nonce":        func(i *factory.HostInvocation) { i.Scope.Nonce = strings.Repeat("f", 32) },
		"scope generation":   func(i *factory.HostInvocation) { i.Scope.Generation++ },
		"scope kind":         func(i *factory.HostInvocation) { i.Scope.Kind = "host" },
		"scope backend":      func(i *factory.HostInvocation) { i.Scope.Backend = "docker" },
		"scope name":         func(i *factory.HostInvocation) { i.Scope.Name += "-other" },
		"deadline":           func(i *factory.HostInvocation) { i.Deadline = i.Deadline.Add(time.Nanosecond) },
	} {
		t.Run(name, func(t *testing.T) {
			i := f.invocation
			i.Args, i.Env = append([]string{}, i.Args...), append([]string{}, i.Env...)
			mutate(&i)
			if validateFactoryCleanupInvocation(t.Context(), f.config, i, f.permit) == nil {
				t.Fatal("changed cleanup command accepted")
			}
		})
	}
}

func testFactoryCleanupInvocationPermit(t *testing.T) {
	f := newFactoryCleanupInvocationFixture(t)
	for name, data := range map[string][]byte{
		"empty":     nil,
		"newline":   append(bytes.Clone(f.invocation.Stdin), '\n'),
		"unknown":   append([]byte(`{"unknown":0,`), f.invocation.Stdin[1:]...),
		"duplicate": append([]byte(`{"operation":"inspect",`), f.invocation.Stdin[1:]...),
		"overlong":  bytes.Repeat([]byte(" "), factory.ManagedCleanupPermitBytes+1),
		"malformed": []byte("{"),
	} {
		t.Run(name, func(t *testing.T) {
			i := f.invocation
			i.Stdin = data
			if validateFactoryCleanupInvocation(t.Context(), f.config, i, f.permit) == nil {
				t.Fatal("noncanonical authenticated permit bytes accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*factory.DockerCleanupPermit){
		"target ID":          func(p *factory.DockerCleanupPermit) { p.Target.ID = strings.Repeat("f", 32) },
		"target nonce":       func(p *factory.DockerCleanupPermit) { p.Target.Nonce = strings.Repeat("f", 32) },
		"target generation":  func(p *factory.DockerCleanupPermit) { p.Target.Generation++ },
		"target backend":     func(p *factory.DockerCleanupPermit) { p.Target.Backend = "launchd" },
		"target kind":        func(p *factory.DockerCleanupPermit) { p.Target.Kind = "container" },
		"target name":        func(p *factory.DockerCleanupPermit) { p.Target.Name += "-other" },
		"binding kind":       func(p *factory.DockerCleanupPermit) { p.Binding.Kind = "container" },
		"container ID":       func(p *factory.DockerCleanupPermit) { p.Binding.ContainerID = strings.Repeat("f", 64) },
		"image":              func(p *factory.DockerCleanupPermit) { p.Binding.ImageID = "sha256:" + strings.Repeat("f", 64) },
		"volume driver":      func(p *factory.DockerCleanupPermit) { p.Binding.VolumeDriver = "foreign" },
		"binding name":       func(p *factory.DockerCleanupPermit) { p.Binding.Name += "-other" },
		"operation":          func(p *factory.DockerCleanupPermit) { p.Operation = factory.DockerCleanupRemove },
		"creation":           func(p *factory.DockerCleanupPermit) { p.Binding.CreatedAt = "2026-01-02T03:04:05Z" },
		"canonical creation": func(p *factory.DockerCleanupPermit) { p.Binding.CreatedAt = "2026-01-02T03:04:05+00:00" },
		"policy": func(p *factory.DockerCleanupPermit) {
			p.Binding.Labels["dev.getoro.factory.policy"] = strings.Repeat("b", 64)
		},
		"context":        func(p *factory.DockerCleanupPermit) { p.Binding.Context = "other-context" },
		"label":          func(p *factory.DockerCleanupPermit) { p.Binding.Labels["extra"] = "unbound" },
		"volume options": func(p *factory.DockerCleanupPermit) { p.Binding.VolumeOptions["o"] += ",exec" },
	} {
		t.Run(name, func(t *testing.T) {
			p := f.permit
			p.Binding = cloneFactoryDockerCleanupBinding(p.Binding)
			mutate(&p)
			if validateFactoryCleanupInvocation(t.Context(), f.config, f.invocation, p) == nil {
				t.Fatal("stdin differed from authenticated authority permit")
			}
		})
	}
	for _, field := range []string{"policy", "context", "objective", "malformed"} {
		t.Run("matching stdin still binds "+field, func(t *testing.T) {
			changed := newFactoryCleanupInvocationFixture(t)
			switch field {
			case "policy":
				changed.permit.Binding.Labels["dev.getoro.factory.policy"] = strings.Repeat("b", 64)
			case "context":
				changed.permit.Binding.Context = "other-context"
			case "objective":
				old := changed.permit.Binding.Labels["dev.getoro.factory.objective"]
				other := strings.Repeat("f", 16)
				changed.permit.Binding.Labels["dev.getoro.factory.objective"] = other
				changed.permit.Scope.Name = strings.Replace(changed.permit.Scope.Name, old, other, 1)
				changed.permit.Target.Name = strings.Replace(changed.permit.Target.Name, old, other, 1)
				changed.permit.Binding.Name = changed.permit.Target.Name
				changed.invocation.Scope = changed.permit.Scope
			case "malformed":
				changed.permit.Binding.CreatedAt = "2026-01-02T03:04:05+00:00"
			}
			changed.encodePermit(t)
			if validateFactoryCleanupInvocation(t.Context(), changed.config, changed.invocation, changed.permit) == nil {
				t.Fatal("permit escaped frozen runtime identity or strict shape")
			}
		})
	}
}

func testFactoryCleanupInvocationConfig(t *testing.T) {
	f := newFactoryCleanupInvocationFixture(t)
	for name, mutate := range map[string]func(*factoryLaunchdConfig){
		"root":            func(c *factoryLaunchdConfig) { c.StateRoot += "-other" },
		"objective":       func(c *factoryLaunchdConfig) { c.ObjectiveID += "-other" },
		"oro path":        func(c *factoryLaunchdConfig) { c.OroPath += "-other" },
		"oro digest":      func(c *factoryLaunchdConfig) { c.OroSHA256 = strings.Repeat("f", 64) },
		"git path":        func(c *factoryLaunchdConfig) { c.GitPath += "-other" },
		"git digest":      func(c *factoryLaunchdConfig) { c.GitSHA256 = strings.Repeat("f", 64) },
		"repository":      func(c *factoryLaunchdConfig) { c.Repository += "-other" },
		"ref":             func(c *factoryLaunchdConfig) { c.TargetRef += "-other" },
		"hard stop":       func(c *factoryLaunchdConfig) { c.HardStop = c.HardStop.Add(time.Second) },
		"cleanup timeout": func(c *factoryLaunchdConfig) { c.CleanupTimeout += time.Nanosecond },
		"artifact digest": func(c *factoryLaunchdConfig) { c.RuntimeConfigSHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			c := f.config
			mutate(&c)
			i := f.invocation
			i.Path, i.Dir = c.OroPath, c.StateRoot
			i.Args = append([]string{}, i.Args...)
			i.Args[3], i.Args[5] = c.RuntimeConfigPath, c.RuntimeConfigSHA256
			if validateFactoryCleanupInvocation(t.Context(), c, i, f.permit) == nil {
				t.Fatal("artifact differs from trusted runtime configuration")
			}
		})
	}
}

func testFactoryCleanupInvocationArtifacts(t *testing.T) {
	for _, kind := range []string{"artifact", "binary", "socket", "context"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactoryCleanupInvocationFixture(t)
			switch kind {
			case "artifact":
				factoryRuntimeConfigWrite(t, f.runtime.path, append(bytes.Clone(f.runtime.data), '\n'))
			case "binary":
				if err := os.Chmod(f.runtime.config.OroPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.runtime.config.OroPath, []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
					t.Fatal(err)
				}
			case "socket":
				if err := os.Chmod(strings.TrimPrefix(f.runtime.config.DockerEndpoint, "unix://"), 0o666); err != nil {
					t.Fatal(err)
				}
			case "context":
				factoryRuntimeConfigWrite(t, f.runtime.metaPath, []byte("{}"))
			}
			if validateFactoryCleanupInvocation(t.Context(), f.config, f.invocation, f.permit) == nil {
				t.Fatal("changed frozen artifact accepted")
			}
		})
	}
}

func testFactoryCleanupInvocationDeadline(t *testing.T) {
	f := newFactoryCleanupInvocationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateFactoryCleanupInvocation(ctx, f.config, f.invocation, f.permit); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation: %v", err)
	}
	for _, deadline := range []time.Time{time.Now().UTC().Add(-time.Second), f.config.HardStop.Add(time.Nanosecond), {}} {
		changed := f
		changed.permit.Deadline, changed.invocation.Deadline = deadline, deadline
		changed.encodePermit(t)
		if validateFactoryCleanupInvocation(t.Context(), changed.config, changed.invocation, changed.permit) == nil {
			t.Fatalf("deadline %s escaped immutable recovery bounds", deadline)
		}
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if err := validateFactoryCleanupInvocation(ctx, f.config, f.invocation, f.permit); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context: %v", err)
	}
}
