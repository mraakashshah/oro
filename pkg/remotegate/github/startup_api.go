package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"oro/pkg/config"
	"oro/pkg/remotegate"
)

// StartupAPI is the read-only GitHub API reader used during dispatcher startup
// preflight. It satisfies APIReader and CollectionReader and lives here rather
// than in cmd: NewClient takes those interfaces, and injecting a cmd-owned
// implementation would make pkg depend on cmd, which .go-arch-lint.yml forbids.
type StartupAPI struct {
	runner     *GHRunner
	host       string
	repository string
}

// StartupAPIConfig carries the resolved values StartupAPI needs. Plain fields
// are used deliberately so no cmd configuration type crosses into pkg.
type StartupAPIConfig struct {
	APIBaseURL      string
	RuntimeIdentity config.GitHubAppIdentityConfig
	Host            string
	Repository      string
	CLIPath         string
	CLIHash         string
}

// NewStartupAPI validates the configured API base URL, builds an attested
// runner backed by CLI-sourced runtime credentials, and returns the reader.
func NewStartupAPI(cfg StartupAPIConfig) (StartupAPI, error) {
	baseURL, err := url.Parse(cfg.APIBaseURL)
	if err != nil {
		return StartupAPI{}, fmt.Errorf("parse GitHub API base URL: %w", err)
	}
	if baseURL.Host == "" {
		return StartupAPI{}, errors.New("GitHub API base URL is missing a host")
	}
	owner := strings.Split(cfg.Repository, "/")[0]
	target := remotegate.CredentialTarget{
		Identity: cfg.RuntimeIdentity,
		Host:     cfg.Host,
		Owner:    owner,
		Name:     strings.TrimPrefix(cfg.Repository, owner+"/"),
	}
	provider := remotegate.NewRuntimeCredentialProvider(target, remotegate.NewCLICredentialSource(cfg.CLIPath))
	runner, err := NewGHRunner(AttestedCLI{
		Path: cfg.CLIPath,
		Hash: cfg.CLIHash,
	}, provider, GHRunnerConfig{Host: cfg.Host})
	if err != nil {
		return StartupAPI{}, fmt.Errorf("construct attested GitHub API runner: %w", err)
	}
	return StartupAPI{runner: runner, host: cfg.Host, repository: cfg.Repository}, nil
}

// GetJSON fetches path and decodes the JSON body into dst.
func (api StartupAPI) GetJSON(ctx context.Context, path string, dst any) error {
	output, err := api.run(ctx, "api", "--hostname", api.host, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(output, dst); err != nil {
		return fmt.Errorf("decode GitHub JSON: %w", err)
	}
	return nil
}

// GetContent fetches raw repository content at path for the given ref.
func (api StartupAPI) GetContent(ctx context.Context, path, ref string) ([]byte, error) {
	endpoint := "repos/" + api.repository + "/contents/" + strings.TrimPrefix(path, "/") + "?ref=" + url.QueryEscape(ref)
	return api.run(ctx, "api", "--hostname", api.host, "--header", "Accept: application/vnd.github.raw+json", endpoint)
}

// CollectJSON fetches a collection, enforcing the request's byte and item caps.
func (api StartupAPI) CollectJSON(ctx context.Context, request CollectionRequest, dst any) (CollectionEvidence, error) {
	output, err := api.run(ctx, "api", "--hostname", api.host, request.Path)
	if err != nil {
		return CollectionEvidence{}, err
	}
	if len(output) > request.MaxBytes {
		return CollectionEvidence{}, errors.New("GitHub policy response exceeds byte limit")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(output, &items); err != nil {
		return CollectionEvidence{}, fmt.Errorf("decode GitHub policy collection: %w", err)
	}
	if len(items) > request.MaxItems {
		return CollectionEvidence{}, errors.New("GitHub policy response exceeds item limit")
	}
	if err := json.Unmarshal(output, dst); err != nil {
		return CollectionEvidence{}, fmt.Errorf("decode GitHub policy collection: %w", err)
	}
	return CollectionEvidence{PageCount: 1, ItemCount: len(items)}, nil
}

func (api StartupAPI) run(ctx context.Context, args ...string) ([]byte, error) {
	request, err := startupAPIRequest(args)
	if err != nil {
		return nil, fmt.Errorf("run GitHub API command: %w", err)
	}
	output, err := api.runner.Run(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("run GitHub API command: %w", err)
	}
	return output, nil
}

// startupAPIRequest translates a gh-style argument vector into an APIRequest.
func startupAPIRequest(args []string) (APIRequest, error) {
	if len(args) < 2 || args[0] != "api" {
		return APIRequest{}, errors.New("unsupported GitHub API command")
	}
	request := APIRequest{Method: "GET"}
	for index := 1; index < len(args); index++ {
		argument := args[index]
		switch argument {
		case "--hostname":
			index++
		case "--header":
			index++
			if index >= len(args) {
				return APIRequest{}, errors.New("GitHub API header is missing a value")
			}
			request.Headers = append(request.Headers, args[index])
			request.Raw = true
		default:
			if strings.HasPrefix(argument, "--") || request.Path != "" {
				return APIRequest{}, errors.New("unsupported GitHub API command")
			}
			request.Path = "/" + strings.TrimPrefix(argument, "/")
		}
	}
	if request.Path == "/" {
		return APIRequest{}, errors.New("GitHub API path is missing")
	}
	return request, nil
}
