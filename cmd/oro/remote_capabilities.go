package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"oro/pkg/config"
)

// RemoteGateConfig is the remote-gate configuration used for capability attestation.
type RemoteGateConfig = config.RemoteGateConfig

// Capabilities records the immutable local and provider observations required
// to safely construct a remote quality-gate client.
type Capabilities struct {
	Host        string               `json:"host"`
	Repository  string               `json:"repository"`
	Workflow    string               `json:"workflow"`
	Permission  RepositoryPermission `json:"permission"`
	GitHubCLI   ExecutableEvidence   `json:"github_cli"`
	Git         GitCapabilities      `json:"git"`
	APILimits   APILimits            `json:"api_limits"`
	MatrixBound int                  `json:"matrix_bound"`
}

// RepositoryPermission records the repository permission required by Oro.
type RepositoryPermission struct {
	Push bool `json:"push"`
}

// ExecutableEvidence identifies one attested executable or helper.
type ExecutableEvidence struct {
	Path       string `json:"path"`
	Version    string `json:"version"`
	Provenance string `json:"provenance"`
	Hash       string `json:"hash"`
}

// GitCapabilities records the trusted Git binary, HTTPS helper, and configured
// credential helper identities observed during attestation.
type GitCapabilities struct {
	Binary            ExecutableEvidence `json:"binary"`
	RemoteHTTPSHelper ExecutableEvidence `json:"remote_https_helper"`
	CredentialHelpers []string           `json:"credential_helpers"`
}

// RateLimit records one GitHub API resource limit.
type RateLimit struct {
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// APILimits records bounded GitHub API resources needed by remote gates.
type APILimits struct {
	Core                      RateLimit `json:"core"`
	ActionsRunnerRegistration RateLimit `json:"actions_runner_registration"`
}

// AttestRemoteCapabilities observes the exact GitHub and Git executables plus
// the configured repository capabilities. It performs no remote mutation.
func AttestRemoteCapabilities(ctx context.Context, cfg RemoteGateConfig) (Capabilities, error) {
	if cfg.Mode != config.RemoteGateModeGitHubPR {
		return Capabilities{}, fmt.Errorf("remote capability attestation requires github-pr mode")
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		return Capabilities{}, fmt.Errorf("locate git: %w", err)
	}
	gitPath, err = canonicalExecutablePath(gitPath)
	if err != nil {
		return Capabilities{}, fmt.Errorf("attest git executable: %w", err)
	}
	remoteURL, err := runCapabilityCommand(ctx, gitPath, "remote", "get-url", cfg.GitHub.Remote)
	if err != nil {
		return Capabilities{}, fmt.Errorf("read git remote %q: %w", cfg.GitHub.Remote, err)
	}
	host, repository, err := remoteRepositoryIdentity(string(remoteURL))
	if err != nil {
		return Capabilities{}, err
	}
	if err := validateConfiguredAPIHost(cfg.GitHub.API.BaseURL, host); err != nil {
		return Capabilities{}, err
	}

	git, err := attestGitCapabilities(ctx, gitPath)
	if err != nil {
		return Capabilities{}, err
	}
	gh, err := attestRemoteGitHubCLI(ctx, cfg.GitHub.CLI.Executable)
	if err != nil {
		return Capabilities{}, err
	}
	repo, err := fetchRepositoryCapability(ctx, gh.Path, host, repository)
	if err != nil {
		return Capabilities{}, err
	}
	if !repo.Permission.Push {
		return Capabilities{}, fmt.Errorf("GitHub repository %s does not grant push permission", repository)
	}
	limits, err := fetchAPILimits(ctx, gh.Path, host)
	if err != nil {
		return Capabilities{}, err
	}
	matrixBound, err := fetchMatrixBound(ctx, gh.Path, host, repository, cfg.GitHub.Workflow)
	if err != nil {
		return Capabilities{}, err
	}
	return Capabilities{
		Host:        host,
		Repository:  repository,
		Workflow:    cfg.GitHub.Workflow,
		Permission:  repo.Permission,
		GitHubCLI:   gh,
		Git:         git,
		APILimits:   limits,
		MatrixBound: matrixBound,
	}, nil
}

// PersistRemoteCapabilities writes new immutable capability evidence. The
// exclusive create rejects an existing path (including a symlink) rather than
// risking overwrite of a target outside Oro's evidence directory.
func PersistRemoteCapabilities(path string, capabilities Capabilities) error {
	data, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("encode remote capabilities: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // caller supplies the project-local Oro directory.
		return fmt.Errorf("create remote capability evidence directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // exclusive evidence creation prevents symlink following.
	if err != nil {
		return fmt.Errorf("create remote capability evidence: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write remote capability evidence: %w", err)
	}
	return nil
}

// VerifyRemoteCapabilities re-attests the local environment and rejects any
// difference from setup's persisted remote capability evidence.
func VerifyRemoteCapabilities(ctx context.Context, cfg RemoteGateConfig, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect remote capability evidence: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("remote capability evidence %s is not a regular file", path)
	}
	data, err := os.ReadFile(path) //nolint:gosec // a regular project-local evidence file was checked above.
	if err != nil {
		return fmt.Errorf("read remote capability evidence: %w", err)
	}
	var persisted Capabilities
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("decode remote capability evidence: %w", err)
	}
	current, err := AttestRemoteCapabilities(ctx, cfg)
	if err != nil {
		return fmt.Errorf("re-attest remote capabilities: %w", err)
	}
	if !reflect.DeepEqual(persisted, current) {
		return fmt.Errorf("remote capability evidence drifted since setup")
	}
	return nil
}

func persistSetupRemoteCapabilities(ctx context.Context, projectRoot string) error {
	cfg, err := config.Load(filepath.Join(projectRoot, ".oro", "config.yaml"))
	if err != nil {
		return fmt.Errorf("load remote gate config: %w", err)
	}
	if cfg.Factory.QualityGate.Mode != config.RemoteGateModeGitHubPR {
		return nil
	}
	evidencePath := remoteCapabilityEvidencePath(projectRoot)
	if info, err := os.Lstat(evidencePath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("remote capability evidence %s is not a regular file", evidencePath)
		}
		return VerifyRemoteCapabilities(ctx, cfg.Factory.QualityGate, evidencePath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect remote capability evidence: %w", err)
	}
	capabilities, err := AttestRemoteCapabilities(ctx, cfg.Factory.QualityGate)
	if err != nil {
		return err
	}
	return PersistRemoteCapabilities(evidencePath, capabilities)
}

func verifyStartupRemoteCapabilities(ctx context.Context, projectRoot string) error {
	cfg, err := config.Load(filepath.Join(projectRoot, ".oro", "config.yaml"))
	if err != nil {
		return fmt.Errorf("load remote gate config: %w", err)
	}
	if cfg.Factory.QualityGate.Mode != config.RemoteGateModeGitHubPR {
		return nil
	}
	if err := VerifyRemoteCapabilities(ctx, cfg.Factory.QualityGate, remoteCapabilityEvidencePath(projectRoot)); err != nil {
		return fmt.Errorf("remote capability startup preflight: %w", err)
	}
	return nil
}

func remoteCapabilityEvidencePath(projectRoot string) string {
	return filepath.Join(projectRoot, ".oro", "remote-capabilities.json")
}

func attestRemoteGitHubCLI(ctx context.Context, configured string) (ExecutableEvidence, error) {
	if configured == "" {
		configured = "gh"
	}
	path, err := exec.LookPath(configured)
	if err != nil {
		return ExecutableEvidence{}, fmt.Errorf("locate configured gh executable %q: %w", configured, err)
	}
	path, err = canonicalExecutablePath(path)
	if err != nil {
		return ExecutableEvidence{}, fmt.Errorf("attest gh executable: %w", err)
	}
	out, err := runCapabilityCommand(ctx, path, "--version")
	if err != nil {
		return ExecutableEvidence{}, fmt.Errorf("read gh version: %w", err)
	}
	version, err := supportedGitHubCLIVersion(string(out))
	if err != nil {
		return ExecutableEvidence{}, err
	}
	return executableEvidence(path, version)
}

func attestGitCapabilities(ctx context.Context, gitPath string) (GitCapabilities, error) {
	versionOut, err := runCapabilityCommand(ctx, gitPath, "--version")
	if err != nil {
		return GitCapabilities{}, fmt.Errorf("read git version: %w", err)
	}
	git, err := executableEvidence(gitPath, strings.TrimSpace(string(versionOut)))
	if err != nil {
		return GitCapabilities{}, err
	}
	execPathOut, err := runCapabilityCommand(ctx, gitPath, "--exec-path")
	if err != nil {
		return GitCapabilities{}, fmt.Errorf("read git exec path: %w", err)
	}
	helperPath, err := canonicalExecutablePath(filepath.Join(strings.TrimSpace(string(execPathOut)), "git-remote-https"))
	if err != nil {
		return GitCapabilities{}, fmt.Errorf("attest git HTTPS helper: %w", err)
	}
	helper, err := executableEvidence(helperPath, "")
	if err != nil {
		return GitCapabilities{}, err
	}
	helpersOut, err := runCapabilityCommand(ctx, gitPath, "config", "--get-all", "credential.helper")
	if err != nil {
		return GitCapabilities{}, fmt.Errorf("read git credential helper identities: %w", err)
	}
	return GitCapabilities{Binary: git, RemoteHTTPSHelper: helper, CredentialHelpers: nonEmptyLines(string(helpersOut))}, nil
}

func executableEvidence(path, version string) (ExecutableEvidence, error) {
	contents, err := os.ReadFile(path) //nolint:gosec // path was canonicalized from a trusted executable discovery.
	if err != nil {
		return ExecutableEvidence{}, fmt.Errorf("read executable %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return ExecutableEvidence{}, fmt.Errorf("stat executable %s: %w", path, err)
	}
	digest := sha256.Sum256(contents)
	return ExecutableEvidence{
		Path:       path,
		Version:    version,
		Provenance: fmt.Sprintf("device=%d,size=%d", fileDevice(info), info.Size()),
		Hash:       hex.EncodeToString(digest[:]),
	}, nil
}

func fileDevice(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev)
	}
	return 0
}

func canonicalExecutablePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make executable path absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlinks: %w", err)
	}
	info, err := os.Stat(resolved) //nolint:gosec // resolved is the canonical path of an executable discovered from trusted configuration.
	if err != nil {
		return "", fmt.Errorf("stat resolved executable: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", resolved)
	}
	return resolved, nil
}

func runCapabilityCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // all executable paths and arguments are attested configuration values.
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func remoteRepositoryIdentity(remote string) (host, repository string, err error) {
	remote = strings.TrimSpace(remote)
	if parsed, err := url.Parse(remote); err == nil && parsed.Hostname() != "" {
		return repositoryIdentity(parsed.Hostname(), parsed.Path)
	}
	if at := strings.LastIndex(remote, "@"); at >= 0 {
		remote = remote[at+1:]
	}
	host, path, ok := strings.Cut(remote, ":")
	if !ok || host == "" {
		return "", "", fmt.Errorf("remote %q has no host and repository", remote)
	}
	return repositoryIdentity(host, path)
}

func repositoryIdentity(host, path string) (repositoryHost, repository string, err error) {
	repository = strings.TrimSuffix(strings.Trim(strings.TrimSpace(path), "/"), ".git")
	if host == "" || repository == "" || !strings.Contains(repository, "/") {
		return "", "", fmt.Errorf("remote identity host=%q repository=%q is incomplete", host, repository)
	}
	return host, repository, nil
}

func validateConfiguredAPIHost(baseURL, host string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("configured GitHub API base URL %q has no host", baseURL)
	}
	if !strings.EqualFold(parsed.Hostname(), host) {
		return fmt.Errorf("configured GitHub API host %q does not match git remote host %q", parsed.Hostname(), host)
	}
	return nil
}

func fetchRepositoryCapability(ctx context.Context, ghPath, host, repository string) (struct{ Permission RepositoryPermission }, error) {
	out, err := runCapabilityCommand(ctx, ghPath, "api", "--hostname", host, "repos/"+repository)
	if err != nil {
		return struct{ Permission RepositoryPermission }{}, fmt.Errorf("read GitHub repository capability: %w", err)
	}
	var response struct {
		FullName    string `json:"full_name"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return struct{ Permission RepositoryPermission }{}, fmt.Errorf("decode GitHub repository capability: %w", err)
	}
	if response.FullName != repository {
		return struct{ Permission RepositoryPermission }{}, fmt.Errorf("GitHub repository identity %q does not match %q", response.FullName, repository)
	}
	return struct{ Permission RepositoryPermission }{Permission: RepositoryPermission{Push: response.Permissions.Push}}, nil
}

func fetchAPILimits(ctx context.Context, ghPath, host string) (APILimits, error) {
	out, err := runCapabilityCommand(ctx, ghPath, "api", "--hostname", host, "rate_limit")
	if err != nil {
		return APILimits{}, fmt.Errorf("read GitHub API limits: %w", err)
	}
	var response struct {
		Resources struct {
			Core                      RateLimit `json:"core"`
			ActionsRunnerRegistration RateLimit `json:"actions_runner_registration"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return APILimits{}, fmt.Errorf("decode GitHub API limits: %w", err)
	}
	if response.Resources.Core.Limit <= 0 {
		return APILimits{}, fmt.Errorf("GitHub core API limit is absent or invalid")
	}
	return APILimits{Core: response.Resources.Core, ActionsRunnerRegistration: response.Resources.ActionsRunnerRegistration}, nil
}

func fetchMatrixBound(ctx context.Context, ghPath, host, repository, workflow string) (int, error) {
	out, err := runCapabilityCommand(ctx, ghPath, "api", "--hostname", host, "repos/"+repository+"/actions/workflows/"+workflow)
	if err != nil {
		return 0, fmt.Errorf("read GitHub workflow capability: %w", err)
	}
	var response struct {
		Path               string `json:"path"`
		State              string `json:"state"`
		MatrixEntriesLimit int    `json:"matrix_entries_limit"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return 0, fmt.Errorf("decode GitHub workflow capability: %w", err)
	}
	if response.Path == "" || response.State != "active" {
		return 0, fmt.Errorf("GitHub workflow %q is not active", workflow)
	}
	if response.MatrixEntriesLimit <= 0 {
		return 0, fmt.Errorf("GitHub workflow %q has no valid matrix entry bound", workflow)
	}
	return response.MatrixEntriesLimit, nil
}

func nonEmptyLines(value string) []string {
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
