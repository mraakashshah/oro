package remotegate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CLICredentialSource resolves runtime tokens by shelling out to an attested
// GitHub CLI. It lives here rather than in cmd so that the concrete
// CredentialSource implementation sits beside the interface it satisfies;
// injecting a cmd-owned type into NewRuntimeCredentialProvider would make pkg
// depend on cmd, which .go-arch-lint.yml forbids.
type CLICredentialSource struct {
	executable string
}

// NewCLICredentialSource builds a source that reads tokens from the attested
// GitHub CLI at executable.
func NewCLICredentialSource(executable string) CLICredentialSource {
	return CLICredentialSource{executable: executable}
}

// Resolve returns a short-lived runtime credential for the requested host.
func (source CLICredentialSource) Resolve(ctx context.Context, request CredentialRequest) (Credential, error) {
	command := exec.CommandContext(ctx, source.executable, "auth", "token", "--hostname", request.Host) //nolint:gosec // executable is validated by the attested GH runner constructor.
	output, err := command.Output()
	if err != nil {
		return Credential{}, fmt.Errorf("read GitHub runtime token: %w", err)
	}
	return Credential{
		Token:          strings.TrimSpace(string(output)),
		Role:           request.Role,
		AppID:          request.Identity.AppID,
		InstallationID: request.Identity.InstallationID,
		Host:           request.Host,
		Owner:          request.Owner,
		Name:           request.Name,
		Permissions:    request.Permissions,
		ExpiresAt:      time.Now().Add(time.Minute),
	}, nil
}
