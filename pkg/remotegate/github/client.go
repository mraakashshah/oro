package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"oro/pkg/remotegate"
)

// Config binds a change adapter to one GitHub repository and exact gate names.
//
//oro:testonly — dispatcher wiring is introduced by subsequent remote-gate tasks.
type Config struct {
	Repository     remotegate.Repository
	RequiredChecks []string
	Limits         CollectionLimits
}

// GitTransport is the narrow internal Git mutation surface used to publish a
// candidate before creating its pull request.
//
//oro:testonly — dispatcher wiring is introduced by subsequent remote-gate tasks.
type GitTransport interface {
	Push(context.Context, remotegate.GitPushRequest) error
}

// Client adapts GitHub pull requests and checks to the provider-neutral remote
// gate lifecycle.
//
//oro:testonly — dispatcher wiring is introduced by subsequent remote-gate tasks.
type Client struct {
	cfg         Config
	gh          *GHRunner
	git         GitTransport
	credentials remotegate.RuntimeCredentialProvider
}

// NewClient constructs a repository-bound GitHub change adapter.
//
//oro:testonly — dispatcher wiring is introduced by subsequent remote-gate tasks.
func NewClient(cfg Config, gh *GHRunner, git GitTransport, creds remotegate.RuntimeCredentialProvider) (*Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if gh == nil || git == nil || gh.config.Host != cfg.Repository.Host {
		return nil, fmt.Errorf("construct GitHub change adapter: %w", remotegate.ErrConfig)
	}
	cfg.RequiredChecks = append([]string(nil), cfg.RequiredChecks...)
	return &Client{cfg: cfg, gh: gh, git: git, credentials: creds}, nil
}

func validateConfig(cfg Config) error {
	repository := cfg.Repository
	if strings.TrimSpace(repository.Host) == "" || strings.TrimSpace(repository.Owner) == "" ||
		strings.TrimSpace(repository.Name) == "" || len(cfg.RequiredChecks) == 0 {
		return fmt.Errorf("construct GitHub change adapter: %w", remotegate.ErrConfig)
	}
	seen := make(map[string]struct{}, len(cfg.RequiredChecks))
	for _, check := range cfg.RequiredChecks {
		check = strings.TrimSpace(check)
		if check == "" {
			return fmt.Errorf("construct GitHub change adapter: %w", remotegate.ErrConfig)
		}
		if _, ok := seen[check]; ok {
			return fmt.Errorf("construct GitHub change adapter: %w", remotegate.ErrConfig)
		}
		seen[check] = struct{}{}
	}
	if cfg.Limits == (CollectionLimits{}) {
		cfg.Limits = CollectionLimits{MaxPages: 100, MaxItems: 1000, MaxBytes: 4 << 20}
	}
	return nil
}

// Publish pushes the exact candidate ref through the isolated Git transport.
func (c *Client) Publish(ctx context.Context, request remotegate.PublishRequest) (remotegate.PublishedCandidate, error) {
	if err := remotegate.ValidateRequest(request); err != nil {
		return remotegate.PublishedCandidate{}, fmt.Errorf("validate candidate publication: %w", err)
	}
	if !c.matchesRepository(request.Candidate.Repository) {
		return remotegate.PublishedCandidate{}, fmt.Errorf("publish candidate: %w", remotegate.ErrInvalidRequest)
	}
	remoteRef := fullHeadRef(request.Candidate.Ref)
	err := c.git.Push(ctx, remotegate.GitPushRequest{
		Operation:            remotegate.GitOperationCandidate,
		LocalRef:             remoteRef,
		RemoteRef:            remoteRef,
		ExpectedRemoteSHA:    request.Lease.ObservedSHA,
		ExpectedRemoteAbsent: request.Lease.ExpectedAbsent,
	})
	if err != nil {
		return remotegate.PublishedCandidate{}, fmt.Errorf("publish candidate: %w", err)
	}
	return remotegate.PublishedCandidate{Candidate: request.Candidate, RemoteRef: remoteRef}, nil
}

// EnsureChange observes before creating, so a lost create response is adopted
// rather than producing a duplicate pull request.
func (c *Client) EnsureChange(ctx context.Context, request remotegate.EnsureChangeRequest) (remotegate.RemoteChange, error) {
	if err := remotegate.ValidateRequest(request); err != nil {
		return remotegate.RemoteChange{}, fmt.Errorf("validate change creation: %w", err)
	}
	if !c.matchesRepository(request.Change.Change.Candidate.Repository) {
		return remotegate.RemoteChange{}, fmt.Errorf("ensure GitHub change: %w", remotegate.ErrInvalidRequest)
	}
	found, err := c.findChange(ctx, request.Change.Change)
	if err != nil {
		return remotegate.RemoteChange{}, err
	}
	if found != nil {
		return c.adoptChange(request.Change, *found)
	}
	created, createErr := c.createDraftChange(ctx, request.Change.Change)
	if createErr == nil {
		return c.adoptChange(request.Change, created)
	}
	found, observeErr := c.findChange(ctx, request.Change.Change)
	if observeErr == nil && found != nil {
		return c.adoptChange(request.Change, *found)
	}
	return remotegate.RemoteChange{}, fmt.Errorf("create GitHub change: %w", createErr)
}

// Observe returns normalized terminal and exact required-check state.
func (c *Client) Observe(ctx context.Context, request remotegate.ObserveGateRequest) (remotegate.RemoteGateObservation, error) {
	if err := remotegate.ValidateRequest(request); err != nil {
		return remotegate.RemoteGateObservation{}, fmt.Errorf("validate change observation: %w", err)
	}
	if !c.matchesRepository(request.Change.Candidate.Repository) {
		return remotegate.RemoteGateObservation{}, fmt.Errorf("observe GitHub change: %w", remotegate.ErrInvalidRequest)
	}
	pr, err := c.getChange(ctx, request.Change.ID)
	if err != nil {
		return remotegate.RemoteGateObservation{}, err
	}
	normalized := pr.normalized(request.Change)
	if !pr.matches(request.Change) {
		return remotegate.RemoteGateObservation{Change: normalized, Terminal: true}, nil
	}
	if pr.State == "closed" {
		return remotegate.RemoteGateObservation{Change: normalized, Terminal: true}, nil
	}
	checks, complete, passed, err := c.observeChecks(ctx, request.Candidate.SHA)
	if err != nil {
		return remotegate.RemoteGateObservation{}, err
	}
	observation := remotegate.RemoteGateObservation{Change: normalized, Terminal: complete, Passed: passed}
	if complete {
		observation.Evidence = remotegate.Evidence{
			ID:            strings.Join(checks, ","),
			Change:        normalized,
			CandidateSHA:  request.Candidate.SHA,
			Target:        request.Target,
			TestedTreeSHA: request.Candidate.TreeSHA,
			PolicyHash:    "github:" + strings.Join(c.cfg.RequiredChecks, ","),
		}
	}
	return observation, nil
}

// SetChangeReady transitions a matching draft once and observes the result
// after both success and ambiguous provider responses.
func (c *Client) SetChangeReady(ctx context.Context, request remotegate.ChangeReadyRequest) (remotegate.RemoteChange, error) {
	if err := remotegate.ValidateRequest(request); err != nil {
		return remotegate.RemoteChange{}, fmt.Errorf("validate ready transition: %w", err)
	}
	pr, err := c.getChange(ctx, request.Change.Change.ID)
	if err != nil {
		return remotegate.RemoteChange{}, err
	}
	if !pr.matches(request.Change.Change) || pr.State == "closed" {
		return remotegate.RemoteChange{}, fmt.Errorf("ready GitHub change: %w", remotegate.ErrDeterministic)
	}
	if !pr.Draft {
		ready := request.Change
		ready.Change.Draft = false
		return ready, nil
	}
	_, readyErr := c.gh.Run(ctx, APIRequest{
		Method: "POST",
		Path:   c.repositoryPath() + "/pulls/" + url.PathEscape(request.Change.Change.ID) + "/ready_for_review",
	})
	observed, observeErr := c.getChange(ctx, request.Change.Change.ID)
	if observeErr == nil && observed.matches(request.Change.Change) && !observed.Draft && observed.State == "open" {
		ready := request.Change
		ready.Change.Draft = false
		return ready, nil
	}
	if readyErr != nil {
		return remotegate.RemoteChange{}, fmt.Errorf("ready GitHub change: %w", readyErr)
	}
	return remotegate.RemoteChange{}, fmt.Errorf("ready GitHub change: %w", remotegate.ErrAmbiguous)
}

// Cancel idempotently closes only the exact matching pull request.
func (c *Client) Cancel(ctx context.Context, request remotegate.CancelGateRequest) error {
	if err := remotegate.ValidateRequest(request); err != nil {
		return fmt.Errorf("validate cancellation: %w", err)
	}
	pr, err := c.getChange(ctx, request.Change.Change.ID)
	if err != nil {
		return err
	}
	if !pr.matches(request.Change.Change) {
		return fmt.Errorf("cancel GitHub change: %w", remotegate.ErrDeterministic)
	}
	if pr.State == "closed" {
		return nil
	}
	return c.closeChange(ctx, request.Change.Change.ID)
}

// Reconcile observes the attempted operation before deciding whether any
// mutation remains necessary.
func (c *Client) Reconcile(ctx context.Context, request remotegate.ReconcileChangeRequest) error {
	if err := remotegate.ValidateRequest(request); err != nil {
		return fmt.Errorf("validate reconciliation: %w", err)
	}
	pr, err := c.getChange(ctx, request.Change.Change.ID)
	if err != nil {
		return err
	}
	if !pr.matches(request.Change.Change) || pr.State == "closed" {
		return nil
	}
	switch request.AttemptedOperation {
	case "cancel":
		return c.closeChange(ctx, request.Change.Change.ID)
	case "set_ready":
		if !pr.Draft {
			return nil
		}
	}
	return fmt.Errorf("reconcile GitHub change: %w", remotegate.ErrAmbiguous)
}

func (c *Client) matchesRepository(repository remotegate.Repository) bool {
	return repository == c.cfg.Repository
}

func (c *Client) repositoryPath() string {
	return "/repos/" + url.PathEscape(c.cfg.Repository.Owner) + "/" + url.PathEscape(c.cfg.Repository.Name)
}

func fullHeadRef(ref string) string {
	if strings.HasPrefix(ref, "refs/heads/") {
		return ref
	}
	return "refs/heads/" + strings.TrimPrefix(ref, "/")
}

type pullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func (pr pullRequest) matches(change remotegate.Change) bool {
	repository := change.Candidate.Repository.Owner + "/" + change.Candidate.Repository.Name
	return strconv.Itoa(pr.Number) == change.ID &&
		pr.Head.Repo.FullName == repository && pr.Base.Repo.FullName == repository &&
		pr.Head.Ref == strings.TrimPrefix(change.Candidate.Ref, "refs/heads/") &&
		pr.Head.SHA == change.Candidate.SHA &&
		pr.Base.Ref == strings.TrimPrefix(change.Target.Ref, "refs/heads/") &&
		pr.Base.SHA == change.Target.SHA
}

func (pr pullRequest) normalized(expected remotegate.Change) remotegate.Change {
	normalized := expected
	normalized.ID = strconv.Itoa(pr.Number)
	normalized.URL = pr.URL
	normalized.Draft = pr.Draft
	return normalized
}

func (c *Client) findChange(ctx context.Context, change remotegate.Change) (*pullRequest, error) {
	query := url.Values{
		"state": {"all"},
		"head":  {c.cfg.Repository.Owner + ":" + strings.TrimPrefix(change.Candidate.Ref, "refs/heads/")},
		"base":  {strings.TrimPrefix(change.Target.Ref, "refs/heads/")},
	}
	output, err := c.gh.Run(ctx, APIRequest{Method: "GET", Path: c.repositoryPath() + "/pulls?" + query.Encode()})
	if err != nil {
		return nil, fmt.Errorf("find GitHub change: %w", err)
	}
	var pulls []pullRequest
	if err := json.Unmarshal(output, &pulls); err != nil {
		return nil, fmt.Errorf("decode GitHub changes: %w", err)
	}
	var match *pullRequest
	for index := range pulls {
		if !pulls[index].matches(change) && change.ID != "" {
			continue
		}
		if pulls[index].Head.SHA != change.Candidate.SHA || pulls[index].Base.SHA != change.Target.SHA {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("find GitHub change: %w", remotegate.ErrAmbiguous)
		}
		match = &pulls[index]
	}
	return match, nil
}

func (c *Client) createDraftChange(ctx context.Context, change remotegate.Change) (pullRequest, error) {
	body, err := json.Marshal(map[string]any{
		"title": "Oro remote gate: " + change.Candidate.Ref,
		"head":  strings.TrimPrefix(change.Candidate.Ref, "refs/heads/"),
		"base":  strings.TrimPrefix(change.Target.Ref, "refs/heads/"),
		"draft": true,
	})
	if err != nil {
		return pullRequest{}, fmt.Errorf("encode GitHub change: %w", err)
	}
	output, err := c.gh.Run(ctx, APIRequest{Method: "POST", Path: c.repositoryPath() + "/pulls", Body: body})
	if err != nil {
		return pullRequest{}, err
	}
	var pr pullRequest
	if err := json.Unmarshal(output, &pr); err != nil {
		return pullRequest{}, fmt.Errorf("decode GitHub change: %w", err)
	}
	return pr, nil
}

func (c *Client) adoptChange(expected remotegate.RemoteChange, pr pullRequest) (remotegate.RemoteChange, error) {
	change := expected.Change
	if change.ID == "" {
		change.ID = strconv.Itoa(pr.Number)
	}
	if !pr.matches(change) || !pr.Draft || pr.State != "open" {
		return remotegate.RemoteChange{}, fmt.Errorf("adopt GitHub change: %w", remotegate.ErrDeterministic)
	}
	change.URL = pr.URL
	change.Draft = true
	expected.Change = change
	return expected, nil
}

func (c *Client) getChange(ctx context.Context, id string) (pullRequest, error) {
	output, err := c.gh.Run(ctx, APIRequest{Method: "GET", Path: c.repositoryPath() + "/pulls/" + url.PathEscape(id)})
	if err != nil {
		return pullRequest{}, fmt.Errorf("get GitHub change: %w", err)
	}
	var pr pullRequest
	if err := json.Unmarshal(output, &pr); err != nil {
		return pullRequest{}, fmt.Errorf("decode GitHub change: %w", err)
	}
	return pr, nil
}

type checkRun struct {
	NodeID     string `json:"node_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// ID returns the stable node identity required by complete collection reads.
func (run checkRun) ID() string {
	return run.NodeID
}

func (c *Client) observeChecks(ctx context.Context, sha string) (ids []string, complete, passed bool, err error) {
	limits := c.cfg.Limits
	if limits == (CollectionLimits{}) {
		limits = CollectionLimits{MaxPages: 100, MaxItems: 1000, MaxBytes: 4 << 20}
	}
	collection, err := Collect[checkRun](ctx, c.gh, CollectionRequest{
		Path:     c.repositoryPath() + "/commits/" + url.PathEscape(sha) + "/check-runs",
		MaxPages: limits.MaxPages,
		MaxItems: limits.MaxItems,
		MaxBytes: limits.MaxBytes,
	})
	if err != nil {
		return nil, false, false, err
	}
	byName := make(map[string]checkRun, len(collection.Items))
	for _, run := range collection.Items {
		if _, exists := byName[run.Name]; exists {
			return nil, false, false, fmt.Errorf("observe GitHub checks: %w", remotegate.ErrAmbiguous)
		}
		byName[run.Name] = run
	}
	ids = make([]string, 0, len(c.cfg.RequiredChecks))
	passed = true
	for _, name := range c.cfg.RequiredChecks {
		run, ok := byName[name]
		if !ok || run.Status != "completed" {
			return nil, false, false, nil
		}
		ids = append(ids, run.NodeID)
		passed = passed && run.Conclusion == "success"
	}
	return ids, true, passed, nil
}

func (c *Client) closeChange(ctx context.Context, id string) error {
	body := json.RawMessage(`{"state":"closed"}`)
	_, err := c.gh.Run(ctx, APIRequest{Method: "PATCH", Path: c.repositoryPath() + "/pulls/" + url.PathEscape(id), Body: body})
	if err == nil {
		return nil
	}
	observed, observeErr := c.getChange(ctx, id)
	if observeErr == nil && observed.State == "closed" {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("close GitHub change: %w", err)
	}
	return fmt.Errorf("close GitHub change: %w", remotegate.ErrAmbiguous)
}
