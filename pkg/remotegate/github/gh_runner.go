// Package github adapts the attested GitHub CLI to the remote-gate boundary.
package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/remotegate"
)

// ErrInvalidAttestation indicates CLI evidence that is not the executable
// recorded during remote-gate setup.
var ErrInvalidAttestation = errors.New("invalid GitHub CLI attestation")

// AttestedCLI is the persisted, setup-attested identity of the GitHub CLI.
// It mirrors only the immutable executable identity used by this runner.
type AttestedCLI struct {
	Path string
	Hash string
}

// GHRunnerConfig configures a GitHub CLI runner without carrying credentials.
type GHRunnerConfig struct {
	Host string
}

// APIRequest describes a token-free GitHub API request.
type APIRequest struct {
	Method string
	Path   string
	Body   json.RawMessage
}

// GHRunner runs the setup-attested GitHub CLI with just-in-time credentials.
type GHRunner struct {
	cli         AttestedCLI
	credentials remotegate.RuntimeCredentialProvider
	config      GHRunnerConfig
}

// NewGHRunner validates persisted CLI evidence before returning a runner.
//
//oro:testonly — production wiring tracked by oro-1e76
func NewGHRunner(cli AttestedCLI, credentials remotegate.RuntimeCredentialProvider, config GHRunnerConfig) (*GHRunner, error) {
	if err := validateAttestedCLI(cli); err != nil {
		return nil, err
	}
	return &GHRunner{cli: cli, credentials: credentials, config: config}, nil
}

// Run resolves a runtime credential before spawning the attested GitHub CLI.
func (runner *GHRunner) Run(ctx context.Context, request APIRequest) (json.RawMessage, error) {
	if runner == nil {
		return nil, fmt.Errorf("run GitHub CLI: %w", ErrInvalidAttestation)
	}
	if err := validateAPIRequest(request); err != nil {
		return nil, err
	}
	credential, err := runner.credentials.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub runtime credential: %w", err)
	}
	configDir, err := os.MkdirTemp("", "oro-gh-config-")
	if err != nil {
		return nil, fmt.Errorf("create isolated GitHub CLI config: %w", err)
	}
	defer os.RemoveAll(configDir)

	args := ghAPIArgs(request, runner.config.Host)
	command := exec.CommandContext(ctx, runner.cli.Path, args...) //nolint:gosec // constructor revalidates the setup-attested absolute executable.
	command.Env = ghEnvironment(configDir, credential.Token)
	if len(request.Body) > 0 {
		command.Stdin = strings.NewReader(string(request.Body))
	}
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("run GitHub CLI: %w", ctx.Err())
		}
		return nil, fmt.Errorf("run GitHub CLI API request: %w", err)
	}
	if !json.Valid(output) {
		return nil, fmt.Errorf("GitHub CLI returned invalid JSON")
	}
	return json.RawMessage(output), nil
}

func validateAttestedCLI(cli AttestedCLI) error {
	if !filepath.IsAbs(cli.Path) || strings.TrimSpace(cli.Hash) == "" {
		return fmt.Errorf("validate GitHub CLI evidence: %w", ErrInvalidAttestation)
	}
	resolved, err := filepath.EvalSymlinks(cli.Path)
	if err != nil {
		return invalidAttestation("resolve executable")
	}
	if resolved != cli.Path {
		return invalidAttestation("executable path is not canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return invalidAttestation("executable is unavailable")
	}
	contents, err := os.ReadFile(resolved) //nolint:gosec // resolved path is validated persisted evidence.
	if err != nil {
		return invalidAttestation("read executable")
	}
	digest := sha256.Sum256(contents)
	if !strings.EqualFold(cli.Hash, hex.EncodeToString(digest[:])) {
		return invalidAttestation("executable digest differs")
	}
	return nil
}

func invalidAttestation(reason string) error {
	return fmt.Errorf("validate GitHub CLI evidence: %w: %s", ErrInvalidAttestation, reason)
}

func validateAPIRequest(request APIRequest) error {
	if strings.TrimSpace(request.Method) == "" || strings.TrimSpace(request.Path) == "" || !strings.HasPrefix(request.Path, "/") {
		return fmt.Errorf("validate GitHub API request: %w", remotegate.ErrInvalidRequest)
	}
	if len(request.Body) > 0 && !json.Valid(request.Body) {
		return fmt.Errorf("validate GitHub API request body: %w", remotegate.ErrInvalidRequest)
	}
	return nil
}

func ghAPIArgs(request APIRequest, host string) []string {
	args := []string{"api", "--method", request.Method}
	if host = strings.TrimSpace(host); host != "" {
		args = append(args, "--hostname", host)
	}
	if len(request.Body) > 0 {
		args = append(args, "--input", "-")
	}
	return append(args, request.Path)
}

func ghEnvironment(configDir, token string) []string {
	return []string{
		"GH_TOKEN=" + token,
		"GH_CONFIG_DIR=" + configDir,
		"HOME=" + configDir,
		"XDG_CONFIG_HOME=" + configDir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(configDir, "gitconfig"),
		"GIT_TERMINAL_PROMPT=0",
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"LANG=C",
		"LC_ALL=C",
	}
}
