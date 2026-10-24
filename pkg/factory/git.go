package factory

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type gitRepository struct {
	path string
}

func (g gitRepository) target(ctx context.Context, branch string) (string, error) {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("invalid target branch %q", branch)
	}
	ref := "refs/heads/" + branch
	if _, err := g.run(ctx, g.path, "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("invalid target branch: %w", err)
	}
	sha, err := g.run(ctx, g.path, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve existing target branch: %w", err)
	}
	worktrees, err := g.run(ctx, g.path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", fmt.Errorf("inspect target worktrees: %w", err)
	}
	for _, field := range strings.Split(worktrees, "\x00") {
		if field == "branch "+ref {
			return "", fmt.Errorf("target branch %q is checked out", branch)
		}
	}
	return sha, nil
}

func (g gitRepository) checkout(ctx context.Context, path, sha string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("checkout path must be absolute: %q", path)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return fmt.Errorf("inspect new checkout path: %w", err)
		}
		return fmt.Errorf("checkout path already exists: %q", path)
	}
	if err := g.exists(ctx, sha); err != nil {
		return err
	}
	if _, err := g.run(ctx, g.path, "worktree", "add", "--detach", "--", path, sha); err != nil {
		return fmt.Errorf("create detached checkout: %w", err)
	}
	return nil
}

func (g gitRepository) commit(ctx context.Context, path, base string) (string, error) {
	if err := g.checkHead(ctx, path, base); err != nil {
		return "", fmt.Errorf("worker changed HEAD: %w", err)
	}
	if _, err := g.run(ctx, path, "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("stage candidate source: %w", err)
	}
	_, err := g.run(ctx, path, "diff", "--cached", "--quiet", "--exit-code", base, "--")
	if err == nil {
		return base, nil
	}
	if !gitExitCode(err, 1) {
		return "", fmt.Errorf("inspect candidate source: %w", err)
	}
	if _, err := g.run(ctx, path, "commit", "--no-gpg-sign", "-m", "factory: candidate"); err != nil {
		return "", fmt.Errorf("commit candidate source: %w", err)
	}
	sha, err := g.run(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve candidate commit: %w", err)
	}
	return sha, nil
}

func (g gitRepository) pristine(ctx context.Context, path, sha string) error {
	if err := g.checkHead(ctx, path, sha); err != nil {
		return fmt.Errorf("checker changed HEAD: %w", err)
	}
	if _, err := g.run(ctx, path, "diff", "--quiet", "--exit-code", sha, "--"); err != nil {
		return fmt.Errorf("checker changed tracked source: %w", err)
	}
	if _, err := g.run(ctx, path, "diff", "--cached", "--quiet", "--exit-code", sha, "--"); err != nil {
		return fmt.Errorf("checker changed tracked index: %w", err)
	}
	return nil
}

func (g gitRepository) checkHead(ctx context.Context, path, sha string) error {
	if err := g.exists(ctx, sha); err != nil {
		return err
	}
	head, err := g.run(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != sha {
		return fmt.Errorf("HEAD %s differs from recorded commit %s", head, sha)
	}
	_, err = g.run(ctx, path, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		return errors.New("candidate HEAD must remain detached")
	}
	if !gitExitCode(err, 1) {
		return fmt.Errorf("inspect detached HEAD: %w", err)
	}
	return nil
}

func (g gitRepository) exists(ctx context.Context, sha string) error {
	if len(sha) != 40 && len(sha) != 64 {
		return fmt.Errorf("expected complete commit object ID, got %q", sha)
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return fmt.Errorf("invalid commit object ID: %w", err)
	}
	resolved, err := g.run(ctx, g.path, "rev-parse", "--verify", sha+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve commit object: %w", err)
	}
	if resolved != sha {
		return fmt.Errorf("object %s is not an exact commit ID", sha)
	}
	return nil
}

func (g gitRepository) run(ctx context.Context, path string, args ...string) (string, error) {
	argv := append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "commit.gpgSign=false",
		"-c", "user.name=Oro Factory",
		"-c", "user.email=factory@oro.invalid",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = path
	cmd.Env = gitEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+7)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "ORO_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true", "GIT_ASKPASS=false")
}

func gitExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}
