/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package forgejoapi is a minimal REST client for the subset of the Forgejo
// API this operator needs to talk to (server info and organizations).
package forgejoapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is returned when the Forgejo API responds with 404.
var ErrNotFound = errors.New("forgejo: resource not found")

// APIError is returned for any other non-2xx response from the Forgejo API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("forgejo: unexpected status %d: %s", e.StatusCode, e.Body)
}

// Client is a small, real HTTP client for the Forgejo REST API, authenticated
// with HTTP basic auth.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// New creates a Client for the Forgejo instance at baseURL, authenticating
// requests with the given username/password.
func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("forgejo: encoding request body: %w", err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("forgejo: building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("forgejo: request to %s %s failed: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("forgejo: reading response body: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("forgejo: decoding response body: %w", err)
		}
	}
	return nil
}

type versionResponse struct {
	Version string `json:"version"`
}

// GetVersion calls the Forgejo info endpoint (/api/v1/version) and returns
// the server version string.
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	var v versionResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// Organization is the subset of the Forgejo organization object this
// operator cares about.
type Organization struct {
	Name                      string `json:"username"`
	FullName                  string `json:"full_name"`
	Visibility                string `json:"visibility"`
	RepoAdminChangeTeamAccess bool   `json:"repo_admin_change_team_access"`
}

// CreateOrgOptions is the request body for creating an organization.
type CreateOrgOptions struct {
	Name                      string `json:"username"`
	Visibility                string `json:"visibility,omitempty"`
	RepoAdminChangeTeamAccess bool   `json:"repo_admin_change_team_access"`
}

// EditOrgOptions is the request body for updating an organization.
type EditOrgOptions struct {
	Visibility                string `json:"visibility,omitempty"`
	RepoAdminChangeTeamAccess bool   `json:"repo_admin_change_team_access"`
}

// GetOrg fetches an organization by name. It returns ErrNotFound if no such
// organization exists.
func (c *Client) GetOrg(ctx context.Context, name string) (*Organization, error) {
	var org Organization
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs/"+url.PathEscape(name), nil, &org); err != nil {
		return nil, err
	}
	return &org, nil
}

// CreateOrg creates a new organization.
func (c *Client) CreateOrg(ctx context.Context, opts CreateOrgOptions) (*Organization, error) {
	var org Organization
	if err := c.do(ctx, http.MethodPost, "/api/v1/orgs", opts, &org); err != nil {
		return nil, err
	}
	return &org, nil
}

// EditOrg updates an existing organization.
//
// Note: as of Forgejo 10, the edit-organization endpoint does not apply
// visibility changes, only newly created organizations honor the requested
// visibility. RepoAdminChangeTeamAccess does apply on edit.
func (c *Client) EditOrg(ctx context.Context, name string, opts EditOrgOptions) error {
	return c.do(ctx, http.MethodPatch, "/api/v1/orgs/"+url.PathEscape(name), opts, nil)
}

// DeleteOrg deletes an organization. It is idempotent: deleting an
// organization that does not exist is not an error.
func (c *Client) DeleteOrg(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/api/v1/orgs/"+url.PathEscape(name), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Forgejo repository unit keys, as used in a team's units_map. These are
// exactly the features Forgejo's own "Custom access" team permission table
// offers: Code, Issues, Pull Requests, Releases, Wiki, Projects, Packages,
// Actions, each independently "none", "read", or "write".
const (
	UnitCode         = "repo.code"
	UnitIssues       = "repo.issues"
	UnitPullRequests = "repo.pulls"
	UnitReleases     = "repo.releases"
	UnitWiki         = "repo.wiki"
	UnitProjects     = "repo.projects"
	UnitPackages     = "repo.packages"
	UnitActions      = "repo.actions"
)

// Team is the subset of the Forgejo team object this operator cares about.
type Team struct {
	ID                      int64             `json:"id"`
	Name                    string            `json:"name"`
	Description             string            `json:"description"`
	Permission              string            `json:"permission"`
	UnitsMap                map[string]string `json:"units_map"`
	CanCreateOrgRepo        bool              `json:"can_create_org_repo"`
	IncludesAllRepositories bool              `json:"includes_all_repositories"`
}

// CreateTeamOptions is the request body for creating a team.
//
// Permission and UnitsMap together select Forgejo's team access mode:
// Permission "admin" grants full administrator access to every unit and
// UnitsMap is ignored; any other Permission value applies UnitsMap as the
// per-unit access level.
type CreateTeamOptions struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description,omitempty"`
	Permission              string            `json:"permission,omitempty"`
	UnitsMap                map[string]string `json:"units_map,omitempty"`
	CanCreateOrgRepo        bool              `json:"can_create_org_repo"`
	IncludesAllRepositories bool              `json:"includes_all_repositories"`
}

// EditTeamOptions is the request body for updating a team.
//
// Permission must be set explicitly even when only changing UnitsMap: if a
// team is currently in "admin" mode, Forgejo ignores any UnitsMap in the
// request unless Permission is also given a non-"admin" value in the same
// request, so callers must always send both together.
type EditTeamOptions struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description,omitempty"`
	Permission              string            `json:"permission,omitempty"`
	UnitsMap                map[string]string `json:"units_map,omitempty"`
	CanCreateOrgRepo        bool              `json:"can_create_org_repo"`
	IncludesAllRepositories bool              `json:"includes_all_repositories"`
}

const teamListPageSize = 50

// ListTeams returns every team in an organization, including the built-in
// "Owners" team, paging through results as needed.
func (c *Client) ListTeams(ctx context.Context, org string) ([]Team, error) {
	var all []Team
	for page := 1; ; page++ {
		var pageTeams []Team
		path := fmt.Sprintf("/api/v1/orgs/%s/teams?page=%d&limit=%d", url.PathEscape(org), page, teamListPageSize)
		if err := c.do(ctx, http.MethodGet, path, nil, &pageTeams); err != nil {
			return nil, err
		}
		all = append(all, pageTeams...)
		if len(pageTeams) < teamListPageSize {
			return all, nil
		}
	}
}

// CreateTeam creates a new team in an organization.
func (c *Client) CreateTeam(ctx context.Context, org string, opts CreateTeamOptions) (*Team, error) {
	var team Team
	if err := c.do(ctx, http.MethodPost, "/api/v1/orgs/"+url.PathEscape(org)+"/teams", opts, &team); err != nil {
		return nil, err
	}
	return &team, nil
}

// EditTeam updates an existing team by ID.
func (c *Client) EditTeam(ctx context.Context, id int64, opts EditTeamOptions) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/teams/%d", id), opts, nil)
}

// DeleteTeam deletes a team by ID. It is idempotent.
func (c *Client) DeleteTeam(ctx context.Context, id int64) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/teams/%d", id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ActionSecret is a Forgejo Actions secret. Its value is write-only: the
// API never returns it, only the name and creation time.
type ActionSecret struct {
	Name string `json:"name"`
}

type upsertActionSecretOptions struct {
	Data string `json:"data"`
}

// UpsertOrgActionSecret creates or updates an organization-level Actions
// secret. This is a single idempotent call: Forgejo returns 201 on create
// and 204 on update, both treated as success.
func (c *Client) UpsertOrgActionSecret(ctx context.Context, org, name, value string) error {
	path := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets/%s", url.PathEscape(org), url.PathEscape(name))
	return c.do(ctx, http.MethodPut, path, upsertActionSecretOptions{Data: value}, nil)
}

// ListOrgActionSecrets returns the names of every Actions secret configured
// on an organization (never their values).
func (c *Client) ListOrgActionSecrets(ctx context.Context, org string) ([]ActionSecret, error) {
	var secrets []ActionSecret
	path := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets", url.PathEscape(org))
	if err := c.do(ctx, http.MethodGet, path, nil, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// DeleteOrgActionSecret deletes an organization-level Actions secret. It is
// idempotent: deleting a secret that does not exist is not an error.
func (c *Client) DeleteOrgActionSecret(ctx context.Context, org, name string) error {
	path := fmt.Sprintf("/api/v1/orgs/%s/actions/secrets/%s", url.PathEscape(org), url.PathEscape(name))
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Repository is the subset of the Forgejo repository object this operator
// cares about.
type Repository struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Template      bool   `json:"template"`
	DefaultBranch string `json:"default_branch"`
}

// CreateRepoOptions is the request body for creating a repository in an
// organization. Unlike EditRepoOptions, AutoInit only applies at creation
// time: it is not a property of the repository afterward.
type CreateRepoOptions struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Private       bool   `json:"private"`
	Template      bool   `json:"template"`
	AutoInit      bool   `json:"auto_init"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// EditRepoOptions is the request body for updating a repository.
type EditRepoOptions struct {
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Template      bool   `json:"template"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// GetRepo fetches a repository by owner (organization) and name. It returns
// ErrNotFound if no such repository exists.
func (c *Client) GetRepo(ctx context.Context, owner, name string) (*Repository, error) {
	var repo Repository
	path := fmt.Sprintf("/api/v1/repos/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	if err := c.do(ctx, http.MethodGet, path, nil, &repo); err != nil {
		return nil, err
	}
	return &repo, nil
}

// CreateOrgRepo creates a new repository in an organization.
func (c *Client) CreateOrgRepo(ctx context.Context, org string, opts CreateRepoOptions) (*Repository, error) {
	var repo Repository
	path := "/api/v1/orgs/" + url.PathEscape(org) + "/repos"
	if err := c.do(ctx, http.MethodPost, path, opts, &repo); err != nil {
		return nil, err
	}
	return &repo, nil
}

// EditRepo updates an existing repository.
func (c *Client) EditRepo(ctx context.Context, owner, name string, opts EditRepoOptions) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	return c.do(ctx, http.MethodPatch, path, opts, nil)
}

// DeleteRepo deletes a repository. It is idempotent: deleting a repository
// that does not exist is not an error.
func (c *Client) DeleteRepo(ctx context.Context, owner, name string) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// DeployKey is a repository deploy key. Unlike organizations, teams, and
// repositories, deploy keys have no edit endpoint: to change one, delete it
// and create a new one.
type DeployKey struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Key      string `json:"key"`
	ReadOnly bool   `json:"read_only"`
}

// CreateDeployKeyOptions is the request body for creating a deploy key.
type CreateDeployKeyOptions struct {
	Title    string `json:"title"`
	Key      string `json:"key"`
	ReadOnly bool   `json:"read_only"`
}

const deployKeyListPageSize = 50

// ListDeployKeys returns every deploy key on a repository, paging through
// results as needed.
func (c *Client) ListDeployKeys(ctx context.Context, owner, repo string) ([]DeployKey, error) {
	var all []DeployKey
	for page := 1; ; page++ {
		var pageKeys []DeployKey
		path := fmt.Sprintf("/api/v1/repos/%s/%s/keys?page=%d&limit=%d",
			url.PathEscape(owner), url.PathEscape(repo), page, deployKeyListPageSize)
		if err := c.do(ctx, http.MethodGet, path, nil, &pageKeys); err != nil {
			return nil, err
		}
		all = append(all, pageKeys...)
		if len(pageKeys) < deployKeyListPageSize {
			return all, nil
		}
	}
}

// CreateDeployKey adds a deploy key to a repository. Forgejo rejects
// (422) adding a key whose content is already registered on the
// repository, so callers should check ListDeployKeys first if the call
// needs to be idempotent.
func (c *Client) CreateDeployKey(ctx context.Context, owner, repo string, opts CreateDeployKeyOptions) (*DeployKey, error) {
	var key DeployKey
	path := fmt.Sprintf("/api/v1/repos/%s/%s/keys", url.PathEscape(owner), url.PathEscape(repo))
	if err := c.do(ctx, http.MethodPost, path, opts, &key); err != nil {
		return nil, err
	}
	return &key, nil
}

// DeleteDeployKey removes a deploy key from a repository by ID. It is
// idempotent: deleting a key that does not exist is not an error (Forgejo
// itself already returns success in that case).
func (c *Client) DeleteDeployKey(ctx context.Context, owner, repo string, id int64) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/keys/%d", url.PathEscape(owner), url.PathEscape(repo), id)
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Hook is a repository webhook.
type Hook struct {
	ID     int64             `json:"id"`
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
	Events []string          `json:"events"`
	Active bool              `json:"active"`
}

// CreateHookOptions is the request body for creating a repository webhook.
type CreateHookOptions struct {
	Type         string            `json:"type"`
	Config       map[string]string `json:"config"`
	Events       []string          `json:"events"`
	Active       bool              `json:"active"`
	BranchFilter string            `json:"branch_filter,omitempty"`
}

// EditHookOptions is the request body for updating a repository webhook.
type EditHookOptions struct {
	Config       map[string]string `json:"config,omitempty"`
	Events       []string          `json:"events,omitempty"`
	Active       bool              `json:"active"`
	BranchFilter string            `json:"branch_filter,omitempty"`
}

const hookListPageSize = 50

// ListRepoHooks returns every webhook on a repository, paging through
// results as needed.
func (c *Client) ListRepoHooks(ctx context.Context, owner, repo string) ([]Hook, error) {
	var all []Hook
	for page := 1; ; page++ {
		var pageHooks []Hook
		path := fmt.Sprintf("/api/v1/repos/%s/%s/hooks?page=%d&limit=%d",
			url.PathEscape(owner), url.PathEscape(repo), page, hookListPageSize)
		if err := c.do(ctx, http.MethodGet, path, nil, &pageHooks); err != nil {
			return nil, err
		}
		all = append(all, pageHooks...)
		if len(pageHooks) < hookListPageSize {
			return all, nil
		}
	}
}

// CreateRepoHook adds a webhook to a repository.
func (c *Client) CreateRepoHook(ctx context.Context, owner, repo string, opts CreateHookOptions) (*Hook, error) {
	var hook Hook
	path := fmt.Sprintf("/api/v1/repos/%s/%s/hooks", url.PathEscape(owner), url.PathEscape(repo))
	if err := c.do(ctx, http.MethodPost, path, opts, &hook); err != nil {
		return nil, err
	}
	return &hook, nil
}

// EditRepoHook updates an existing webhook by ID.
func (c *Client) EditRepoHook(ctx context.Context, owner, repo string, id int64, opts EditHookOptions) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(repo), id)
	return c.do(ctx, http.MethodPatch, path, opts, nil)
}

// DeleteRepoHook removes a webhook from a repository by ID. It is
// idempotent: deleting a webhook that does not exist is not an error.
func (c *Client) DeleteRepoHook(ctx context.Context, owner, repo string, id int64) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(repo), id)
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// RegisterRunnerOptions is the request body for registering a new Actions
// runner. Unlike a plain registration token, this endpoint (added in
// Forgejo 15) performs the full registration handshake in one plain REST
// call and returns usable runner credentials directly -- no separate
// exchange over the runner's own Connect-RPC protocol is needed.
type RegisterRunnerOptions struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Ephemeral   bool   `json:"ephemeral"`
}

// RegisterRunnerResponse contains the credentials of a just-registered
// Actions runner. Token is long-lived: it is the same secret a
// `forgejo-runner register` CLI invocation would have written into a
// ".runner" file, and is not retrievable again after this call returns.
type RegisterRunnerResponse struct {
	ID    int64  `json:"id"`
	UUID  string `json:"uuid"`
	Token string `json:"token"`
}

// RegisterOrgRunner registers a new Actions runner on an organization. Each
// call creates a brand new runner registration; it is not idempotent, so
// callers must persist the returned credentials (they cannot be fetched
// again) and only call this once per runner.
func (c *Client) RegisterOrgRunner(ctx context.Context, org string, opts RegisterRunnerOptions) (*RegisterRunnerResponse, error) {
	var resp RegisterRunnerResponse
	path := "/api/v1/orgs/" + url.PathEscape(org) + "/actions/runners"
	if err := c.do(ctx, http.MethodPost, path, opts, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteOrgRunner removes a runner from an organization by ID. It is
// idempotent: deleting a runner that does not exist is not an error.
func (c *Client) DeleteOrgRunner(ctx context.Context, org string, id int64) error {
	path := fmt.Sprintf("/api/v1/orgs/%s/actions/runners/%d", url.PathEscape(org), id)
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Runner is the subset of a registered Forgejo Actions runner this operator
// cares about.
type Runner struct {
	ID     int64    `json:"id"`
	UUID   string   `json:"uuid"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// GetOrgRunner fetches a single runner registered on an organization by ID.
// It returns ErrNotFound if no such runner exists.
func (c *Client) GetOrgRunner(ctx context.Context, org string, id int64) (*Runner, error) {
	var runner Runner
	path := fmt.Sprintf("/api/v1/orgs/%s/actions/runners/%d", url.PathEscape(org), id)
	if err := c.do(ctx, http.MethodGet, path, nil, &runner); err != nil {
		return nil, err
	}
	return &runner, nil
}
