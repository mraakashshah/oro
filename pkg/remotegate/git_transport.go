package remotegate

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitTransportCapabilities is the immutable Git executable evidence consumed
// by the internal remote-gate transport.
//
//oro:testonly — dispatcher wiring is introduced by a subsequent task.
type GitTransportCapabilities struct {
	BinaryPath            string
	RemoteHTTPSHelperPath string
}

// GitOperation identifies the only ref classes the internal transport may
// mutate.
//
//oro:testonly — dispatcher wiring is introduced by a subsequent task.
type GitOperation string

const (
	// GitOperationCandidate publishes a dispatcher candidate ref.
	GitOperationCandidate GitOperation = "candidate"
	// GitOperationEpic publishes a dispatcher epic ref.
	GitOperationEpic GitOperation = "epic"
	// GitOperationAudit publishes a dispatcher audit ref.
	GitOperationAudit GitOperation = "audit"
	// GitOperationTargetCAS updates a non-internal target ref with a lease.
	GitOperationTargetCAS GitOperation = "target-cas"
)

// GitPushRequest carries one internal ref update and its exact remote lease.
//
//oro:testonly — dispatcher wiring is introduced by a subsequent task.
type GitPushRequest struct {
	Operation            GitOperation
	LocalRef             string
	RemoteRef            string
	ExpectedRemoteSHA    string
	ExpectedRemoteAbsent bool
}

type internalGitTransport struct {
	capabilities     Capabilities
	credentials      RuntimeCredentialProvider
	remoteURL        string
	workingDirectory string
}

// newInternalGitTransport constructs the capability-bound internal Git
// transport. It intentionally accepts only persisted capability evidence and
// a runtime credential provider; ambient Git configuration is never trusted.
//
//nolint:revive,gocritic // The constructor parameter name is fixed by the transport contract.
func newInternalGitTransport(cap Capabilities, creds RuntimeCredentialProvider) *internalGitTransport {
	return &internalGitTransport{capabilities: cap, credentials: creds, remoteURL: internalGitRemoteURL(cap.Repository)}
}

// Push updates one dispatcher-owned ref with an exact force-with-lease.
func (transport *internalGitTransport) Push(ctx context.Context, request GitPushRequest) error {
	if transport == nil {
		return fmt.Errorf("push internal Git ref: %w", ErrConfig)
	}
	if err := validateGitPushRequest(request); err != nil {
		return err
	}
	if err := validateGitTransportCapabilities(transport.capabilities); err != nil {
		return err
	}
	credential, err := transport.credentials.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("resolve Git runtime credential: %w", err)
	}
	//nolint:gosec // The binary and every argument are setup-attested or validated as exact internal refs and leases.
	command := exec.CommandContext(ctx, transport.capabilities.Git.BinaryPath,
		"push",
		gitForceWithLease(request),
		transport.remoteURL,
		request.LocalRef+":"+request.RemoteRef,
	)
	command.Dir = transport.workingDirectory
	command.Env = internalGitEnvironment(transport.capabilities, credential)
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("push internal Git ref: %w", ctx.Err())
		}
		return fmt.Errorf("push internal Git ref: %w", err)
	}
	return nil
}

func validateGitPushRequest(request GitPushRequest) error {
	if !supportedGitOperation(request.Operation) {
		return fmt.Errorf("push internal Git ref: unsupported operation %q: %w", request.Operation, ErrInvalidRequest)
	}
	if !matchesGitOperation(request.Operation, request.LocalRef) || !matchesGitOperation(request.Operation, request.RemoteRef) {
		return fmt.Errorf("push internal Git ref: ref is not dispatcher-owned: %w", ErrInvalidRequest)
	}
	if request.ExpectedRemoteAbsent == (request.ExpectedRemoteSHA != "") || (!request.ExpectedRemoteAbsent && !isGitSHA(request.ExpectedRemoteSHA)) {
		return fmt.Errorf("push internal Git ref: exact lease is required: %w", ErrInvalidRequest)
	}
	return nil
}

func gitForceWithLease(request GitPushRequest) string {
	return "--force-with-lease=" + request.RemoteRef + ":" + request.ExpectedRemoteSHA
}

func supportedGitOperation(operation GitOperation) bool {
	switch operation {
	case GitOperationCandidate, GitOperationEpic, GitOperationAudit, GitOperationTargetCAS:
		return true
	default:
		return false
	}
}

func matchesGitOperation(operation GitOperation, ref string) bool {
	if !validGitRef(ref) {
		return false
	}
	switch operation {
	case GitOperationCandidate:
		return strings.HasPrefix(ref, "refs/heads/agent/")
	case GitOperationEpic:
		return strings.HasPrefix(ref, "refs/heads/epic/")
	case GitOperationAudit:
		return strings.HasPrefix(ref, "refs/heads/audit/")
	case GitOperationTargetCAS:
		return strings.HasPrefix(ref, "refs/heads/") && !isInternalGitRef(ref)
	default:
		return false
	}
}

func isInternalGitRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/heads/agent/") || strings.HasPrefix(ref, "refs/heads/epic/") || strings.HasPrefix(ref, "refs/heads/audit/")
}

func validGitRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/") && !strings.ContainsAny(ref, " \t\r\n~^:?*[\\") && !strings.Contains(ref, "..") && !strings.HasSuffix(ref, ".")
}

func isGitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validateGitTransportCapabilities(capabilities Capabilities) error {
	git := capabilities.Git
	if capabilities.Repository.Host == "" || capabilities.Repository.Owner == "" || capabilities.Repository.Name == "" || !filepath.IsAbs(git.BinaryPath) || !filepath.IsAbs(git.RemoteHTTPSHelperPath) || filepath.Base(git.RemoteHTTPSHelperPath) != "git-remote-https" {
		return fmt.Errorf("validate internal Git capabilities: %w", ErrConfig)
	}
	return nil
}

func internalGitRemoteURL(repository Repository) string {
	return "https://" + repository.Host + "/" + repository.Owner + "/" + repository.Name + ".git"
}

func internalGitEnvironment(capabilities Capabilities, credential Credential) []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"GIT_EXEC_PATH=" + filepath.Dir(capabilities.Git.RemoteHTTPSHelperPath),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=http.https://" + capabilities.Repository.Host + "/.extraheader",
		"GIT_CONFIG_VALUE_1=Authorization: Bearer " + credential.Token,
		"LANG=C",
		"LC_ALL=C",
	}
}
