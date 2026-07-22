// Package codex implements Oro runtime adapters for the Codex CLI.
package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/agentruntime"
	"oro/pkg/ops"
	"oro/pkg/processenv"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

const commandName = "codex"

// Runtime describes Codex runtime capabilities and defaults.
type Runtime struct{}

// NewRuntime creates a Codex runtime descriptor.
//
//oro:testonly — production registry wiring is deferred to oro-hmm8 spawn unification.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// ID reports the stable Codex runtime identifier.
func (r *Runtime) ID() agentruntime.RuntimeID {
	return agentruntime.RuntimeIDCodex
}

// DefaultTierModel leaves model resolution to the configured role/tier resolver.
func (r *Runtime) DefaultTierModel(role string, tier protocol.Tier) string {
	return ""
}

// StreamFormat reports Codex's plain line-oriented stdout contract.
func (r *Runtime) StreamFormat() agentruntime.StreamFormat {
	return agentruntime.StreamFormatLineText
}

// InstructionLayout returns the default instruction layout placeholder.
func (r *Runtime) InstructionLayout() agentruntime.InstructionLayout {
	return agentruntime.InstructionLayout{}
}

// SupportsHooks reports that Codex does not support project hook configuration.
func (r *Runtime) SupportsHooks() bool {
	return false
}

// SupportsProjectSkillInstall reports that Codex does not support project skill installs.
func (r *Runtime) SupportsProjectSkillInstall() bool {
	return false
}

// WorkerSpawner implements the worker.StreamingSpawner contract for Codex.
type WorkerSpawner struct {
	command string
}

// NewWorkerSpawner creates the Codex worker spawner.
func NewWorkerSpawner() *WorkerSpawner {
	return &WorkerSpawner{command: commandName}
}

// StreamFormat reports the Codex stdout contract used by Oro v1.
func (s *WorkerSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatLineText
}

// Spawn starts a `codex exec` subprocess using a plain-text line stream.
func (s *WorkerSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	return s.SpawnWithReasoning(ctx, model, "", prompt, workdir)
}

// SpawnWithReasoning starts Codex with an optional model reasoning effort.
func (s *WorkerSpawner) SpawnWithReasoning(ctx context.Context, model, reasoning, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	return s.SpawnWithLaunchPolicy(ctx, model, reasoning, prompt, workdir, worker.LaunchPolicyDefault)
}

// SpawnWithLaunchPolicy starts Codex and verifies managed hook activation for
// read-only Oracle launches before returning the process.
func (s *WorkerSpawner) SpawnWithLaunchPolicy(ctx context.Context, model, reasoning, prompt, workdir string, policy worker.LaunchPolicy) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	if policy != worker.LaunchPolicyDefault && policy != worker.LaunchPolicyReadOnly {
		return nil, nil, nil, fmt.Errorf("unknown launch policy %q", policy)
	}
	assembledPrompt := strings.ToValidUTF8(BuildBootstrapPrompt(prompt, workdir), "�")
	cmd := exec.CommandContext(ctx, s.binary(), buildWorkerExecArgsWithReasoning(model, reasoning, workdir)...) //nolint:gosec // args built internally
	cmd.Dir = workdir
	stderrTail := worker.NewLineTailBuffer(100)
	cmd.Stderr = io.MultiWriter(os.Stderr, stderrTail)
	cmd.Env = worker.EnvironmentForContext(ctx, processenv.ForWorkdir(os.Environ(), workdir))
	var probe *worker.OracleHookProbe
	if policy == worker.LaunchPolicyReadOnly {
		var err error
		probe, err = worker.NewOracleHookProbe()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("create Oracle hook probe: %w", err)
		}
		cmd.Env = append(cmd.Env, probe.Environment())
	}

	cmd.Stdin = strings.NewReader(assembledPrompt)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if probe != nil {
			_ = os.RemoveAll(filepath.Dir(probe.MarkerPath()))
		}
		return nil, nil, nil, wrapStartError(err)
	}
	proc := worker.Process(&worker.CmdProcess{Cmd: cmd, Runtime: commandName, Stderr: stderrTail})
	if probe != nil {
		replayable := worker.NewReplayableProcess(proc)
		if err := probe.Await(ctx, replayable, 5*time.Second); err != nil {
			return nil, nil, nil, fmt.Errorf("await Oracle hook activation: %w", err)
		}
		proc = replayable
	}
	return proc, stdoutPipe, nil, nil
}

func (s *WorkerSpawner) binary() string {
	if s == nil || s.command == "" {
		return commandName
	}
	return s.command
}

// NewOpsSpawner creates the Codex ops spawner using the same subprocess contract.
func NewOpsSpawner() ops.BatchSpawner {
	return ops.NewExecSpawner(ops.RuntimeSpec{
		Command:                commandName,
		BuildArgs:              buildExecArgs,
		BuildArgsWithReasoning: buildExecArgsWithReasoning,
	})
}

func buildExecArgs(model, prompt string) []string {
	return buildExecArgsWithReasoning(model, "", prompt)
}

func buildExecArgsWithReasoning(model, reasoning, prompt string) []string {
	args := buildExecArgPrefix(model, reasoning)
	args = append(args, prompt)
	return args
}

func buildWorkerExecArgsWithReasoning(model, reasoning, workdir string) []string {
	args := buildExecArgPrefixWithSandbox(model, reasoning, "danger-full-access")
	if gitCommonDir := resolveGitCommonDir(workdir); gitCommonDir != "" {
		args = append(args, "--add-dir", gitCommonDir)
		if gitDir := resolveGitDir(workdir); gitDir != "" && gitDir != gitCommonDir {
			args = append(args, "--add-dir", gitDir)
		}
	}
	args = append(args, "-")
	return args
}

func buildExecArgPrefix(model, reasoning string) []string {
	return buildExecArgPrefixWithSandbox(model, reasoning, "danger-full-access")
}

func buildExecArgPrefixWithSandbox(model, reasoning, sandbox string) []string {
	// --dangerously-bypass-hook-trust: codex-cli gates config-file PreToolUse
	// hooks behind a persisted per-hook trusted_hash. codex exec is
	// non-interactive, so without this flag NONE of oro's managed hooks
	// (oro-search-hook, enforce_skills, destructive_command_guard, …) ever fire.
	// The flag is intended for automation that vets its own hook sources — oro
	// authors and installs these hooks itself. Trust does not persist across
	// runs, so the flag must ride every spawn.
	//
	// Scope caveat: the flag bypasses the trust gate for EVERY hook in the active
	// $CODEX_HOME/config.toml, not only oro's managed block. This is contained
	// because oro workers spawn into an oro-managed CODEX_HOME. Requires a
	// codex-cli new enough to recognize the flag (verified on 0.144.6); an older
	// codex would reject the unknown flag and fail the spawn.
	args := []string{"exec", "--skip-git-repo-check", "--sandbox", sandbox, "--dangerously-bypass-hook-trust"}
	model = normalizeCodexModel(model)
	if model != "" {
		args = append(args, "--model", model)
	}
	reasoning = strings.TrimSpace(reasoning)
	if reasoning != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", reasoning))
	}
	return args
}

func resolveGitCommonDir(workdir string) string {
	return resolveGitDirFlag(workdir, "--git-common-dir")
}

func resolveGitDir(workdir string) string {
	return resolveGitDirFlag(workdir, "--git-dir")
}

func resolveGitDirFlag(workdir, flag string) string {
	if strings.TrimSpace(workdir) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "rev-parse", "--path-format=absolute", flag) //nolint:gosec // fixed git invocation
	cmd.Env = processenv.ForWorkdir(os.Environ(), workdir)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

func normalizeCodexModel(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, legacy := range []string{"sonnet", "opus", "haiku"} {
		if lower == legacy || strings.Contains(lower, legacy) {
			return ""
		}
	}
	return strings.TrimSpace(model)
}

// BuildBootstrapPrompt prepends shared Oro guidance for Codex runs without relying
// on Claude hook surfaces. It prefers worktree-local ORO_AGENT.md and falls back
// to $ORO_HOME/ORO_AGENT.md when available.
func BuildBootstrapPrompt(prompt, workdir string) string {
	if shared := readSharedInstructions(workdir); shared != "" {
		return strings.TrimSpace(shared) + "\n\n## Task\n\n" + prompt
	}
	return prompt
}

func readSharedInstructions(workdir string) string {
	candidates := []string{}
	if workdir != "" {
		candidates = append(candidates, filepath.Join(workdir, "ORO_AGENT.md"))
	}
	if oroHome := os.Getenv("ORO_HOME"); oroHome != "" {
		candidates = append(candidates, filepath.Join(oroHome, "ORO_AGENT.md"))
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate) //nolint:gosec // internally constructed candidate paths
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return string(data)
		}
	}
	return ""
}

func wrapStartError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("start codex: codex CLI not found; install Codex or set %s=%s: %w", "ORO_AGENT_RUNTIME", "claude", err)
	}
	return fmt.Errorf("start codex: %w", err)
}
