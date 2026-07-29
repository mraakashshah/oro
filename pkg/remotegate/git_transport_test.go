package remotegate //nolint:testpackage // Exercises the unexported internal transport contract.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
)

func TestInternalGitTransportIsolation(t *testing.T) {
	binDir := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	envPath := filepath.Join(t.TempDir(), "env")
	gitPath := writeGitTransportFixture(t, binDir, "git", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nenv | sort > %s\n", argsPath, envPath))
	helperPath := writeGitTransportFixture(t, binDir, "git-remote-https", "#!/bin/sh\nexit 0\n")
	for key, value := range map[string]string{
		"GIT_EXEC_PATH":                    "/poison/exec",
		"PATH":                             "/poison/path",
		"GIT_CONFIG_GLOBAL":                "/poison/config",
		"GIT_CONFIG_SYSTEM":                "/poison/system-config",
		"GIT_CONFIG_NOSYSTEM":              "0",
		"GIT_SSH":                          "/poison/ssh",
		"GIT_SSH_COMMAND":                  "/poison/ssh command",
		"GIT_OBJECT_DIRECTORY":             "/poison/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": "/poison/alternates",
		"LD_PRELOAD":                       "/poison/loader",
	} {
		t.Setenv(key, value)
	}

	target := CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{Type: "github-app", AppID: 1, InstallationID: 2, PrivateKeyRef: "keychain:oro/test"},
		Host:     "github.example",
		Owner:    "acme",
		Name:     "oro",
	}
	transport := newInternalGitTransport(Capabilities{
		Repository: Repository{Host: target.Host, Owner: target.Owner, Name: target.Name},
		Git:        GitTransportCapabilities{BinaryPath: gitPath, RemoteHTTPSHelperPath: helperPath},
	}, NewRuntimeCredentialProvider(target, gitTransportCredentialSource{target: target}))

	for _, request := range []GitPushRequest{
		{Operation: GitOperationCandidate, LocalRef: "refs/heads/agent/a", RemoteRef: "refs/heads/agent/a", ExpectedRemoteSHA: "1111111111111111111111111111111111111111"},
		{Operation: GitOperationEpic, LocalRef: "refs/heads/epic/a", RemoteRef: "refs/heads/epic/a", ExpectedRemoteSHA: "2222222222222222222222222222222222222222"},
		{Operation: GitOperationAudit, LocalRef: "refs/heads/audit/a", RemoteRef: "refs/heads/audit/a", ExpectedRemoteSHA: "3333333333333333333333333333333333333333"},
		{Operation: GitOperationTargetCAS, LocalRef: "refs/heads/main", RemoteRef: "refs/heads/main", ExpectedRemoteSHA: "4444444444444444444444444444444444444444"},
	} {
		if err := transport.Push(context.Background(), request); err != nil {
			t.Fatalf("Push(%s) error = %v", request.Operation, err)
		}
		args, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatalf("read git args: %v", err)
		}
		lease := "--force-with-lease=" + request.RemoteRef + ":" + request.ExpectedRemoteSHA
		if !strings.Contains(string(args), lease) {
			t.Fatalf("Push(%s) args = %q, want exact lease %q", request.Operation, args, lease)
		}
	}

	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read git environment: %v", err)
	}
	env := string(environment)
	for _, want := range []string{
		"GIT_EXEC_PATH=" + binDir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=http.https://github.example/.extraheader",
		"GIT_CONFIG_VALUE_1=Authorization: Bearer runtime-token",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("isolated environment missing %q:\n%s", want, env)
		}
	}
	for _, forbidden := range []string{"/poison/", "GIT_SSH=", "GIT_SSH_COMMAND=", "GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "LD_PRELOAD="} {
		if strings.Contains(env, forbidden) {
			t.Errorf("isolated environment leaked %q:\n%s", forbidden, env)
		}
	}
	if got := os.Getenv("GIT_CONFIG_GLOBAL"); got != "/poison/config" {
		t.Errorf("ambient Git configuration = %q, want unchanged user configuration", got)
	}

	for _, request := range []GitPushRequest{
		{Operation: GitOperationCandidate, LocalRef: "refs/heads/agent/a", RemoteRef: "refs/heads/agent/a"},
		{Operation: GitOperation("unsupported"), LocalRef: "refs/heads/a", RemoteRef: "refs/heads/a", ExpectedRemoteSHA: "1111111111111111111111111111111111111111"},
		{Operation: GitOperationTargetCAS, LocalRef: "refs/heads/agent/a", RemoteRef: "refs/heads/agent/a", ExpectedRemoteSHA: "1111111111111111111111111111111111111111"},
	} {
		if err := transport.Push(context.Background(), request); err == nil {
			t.Errorf("Push(%#v) error = nil, want rejection", request)
		}
	}
}

type gitTransportCredentialSource struct{ target CredentialTarget }

func (source gitTransportCredentialSource) Resolve(_ context.Context, request CredentialRequest) (Credential, error) {
	return Credential{Token: "runtime-token", Role: request.Role, AppID: source.target.Identity.AppID, InstallationID: source.target.Identity.InstallationID, Host: source.target.Host, Owner: source.target.Owner, Name: source.target.Name, Permissions: request.Permissions, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func writeGitTransportFixture(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}
