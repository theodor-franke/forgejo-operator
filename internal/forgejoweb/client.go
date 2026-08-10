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

// Package forgejoweb drives Forgejo's web admin UI for the one piece of
// configuration that has no REST API: an authentication source's "map
// claimed groups to organization teams" setting. It authenticates as a
// regular browser session (login form + CSRF token + cookie jar) and
// scrapes/resubmits the admin auth-source edit form, since Forgejo does not
// expose authentication sources over /api/v1 at all.
package forgejoweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ErrNoOAuth2Source is returned when no OAuth2/OIDC authentication source is
// configured on the instance, so there is nothing to sync the group-team map
// to.
var ErrNoOAuth2Source = errors.New("forgejo web: no OAuth2 authentication source configured")

// Client drives Forgejo's web admin UI over an authenticated browser
// session.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	loggedIn   bool
}

// New creates a Client for the Forgejo instance at baseURL, authenticating
// as username/password through the normal web login form.
func New(baseURL, username, password string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("forgejo web: creating cookie jar: %w", err)
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
			// We always know exactly which page to fetch next; intercept
			// redirects ourselves so we can tell a successful form
			// submission (303) apart from a re-rendered form with a
			// validation error (200).
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("forgejo web: building request for %s: %w", path, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forgejo web: fetching %s: %w", path, err)
	}
	return resp, nil
}

func (c *Client) postForm(ctx context.Context, path string, values url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("forgejo web: building request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forgejo web: posting to %s: %w", path, err)
	}
	return resp, nil
}

// ensureLoggedIn performs the web login flow once per Client and caches the
// resulting session cookie for subsequent requests.
func (c *Client) ensureLoggedIn(ctx context.Context) error {
	if c.loggedIn {
		return nil
	}

	resp, err := c.get(ctx, "/user/login")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("forgejo web: fetching login page: unexpected status %d", resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return fmt.Errorf("forgejo web: parsing login page: %w", err)
	}
	// Forgejo's login form does not always carry a CSRF token: newer
	// versions (verified against 15.0.6, gitea 1.22 base) no longer embed
	// one, since login CSRF is a different, less severe threat class than
	// CSRF against an already-authenticated session. Include it when
	// present (older versions, e.g. the forgejo:10 test image, still
	// require it) and omit it otherwise.
	values := url.Values{
		"user_name": {c.username},
		"password":  {c.password},
	}
	if csrf, ok := findCSRFToken(doc); ok {
		values.Set("_csrf", csrf)
	}

	loginResp, err := c.postForm(ctx, "/user/login", values)
	if err != nil {
		return err
	}
	defer func() { _ = loginResp.Body.Close() }()

	if isRedirect(loginResp.StatusCode) {
		c.loggedIn = true
		return nil
	}
	return fmt.Errorf("forgejo web: login failed: %s", describeFormFailure(loginResp))
}

// FirstOAuth2AuthSourceID returns the ID of the first (lowest-ID) OAuth2/OIDC
// authentication source configured on the instance. It returns
// ErrNoOAuth2Source if none is configured; the group-team map only applies
// to OAuth2 sources, so other source types (LDAP, SMTP, ...) are ignored.
func (c *Client) FirstOAuth2AuthSourceID(ctx context.Context) (int, error) {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return 0, err
	}

	resp, err := c.get(ctx, "/admin/auths")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("forgejo web: listing authentication sources: unexpected status %d", resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("forgejo web: parsing authentication source list: %w", err)
	}

	id, ok := findFirstOAuth2SourceID(doc)
	if !ok {
		return 0, ErrNoOAuth2Source
	}
	return id, nil
}

// SetGroupTeamMap updates the "map claimed groups to organization teams"
// setting on the given OAuth2 authentication source, preserving every other
// field already configured on it (the edit form is a full resubmit, not a
// partial patch).
func (c *Client) SetGroupTeamMap(ctx context.Context, sourceID int, groupTeamMapJSON string) error {
	if err := c.ensureLoggedIn(ctx); err != nil {
		return err
	}

	editPath := fmt.Sprintf("/admin/auths/%d", sourceID)

	resp, err := c.get(ctx, editPath)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("forgejo web: fetching auth source %d edit form: unexpected status %d", sourceID, resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return fmt.Errorf("forgejo web: parsing auth source %d edit form: %w", sourceID, err)
	}

	form, ok := findForm(doc, editPath)
	if !ok {
		return fmt.Errorf("forgejo web: could not find edit form for auth source %d", sourceID)
	}

	values := extractFormValues(form)
	values.Set("oauth2_group_team_map", groupTeamMapJSON)

	submitResp, err := c.postForm(ctx, editPath, values)
	if err != nil {
		return err
	}
	defer func() { _ = submitResp.Body.Close() }()

	if isRedirect(submitResp.StatusCode) {
		return nil
	}
	return fmt.Errorf("forgejo web: updating auth source %d failed: %s", sourceID, describeFormFailure(submitResp))
}

func isRedirect(status int) bool {
	return status == http.StatusSeeOther || status == http.StatusFound
}

// describeFormFailure extracts the flash-error message from a re-rendered
// form page, falling back to the raw status code when none is found.
func describeFormFailure(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("unexpected status %d (and could not read body: %v)", resp.StatusCode, err)
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return fmt.Sprintf("unexpected status %d", resp.StatusCode)
	}
	if msg, ok := findFlashError(doc); ok {
		return msg
	}
	return fmt.Sprintf("unexpected status %d", resp.StatusCode)
}

// --- HTML scraping helpers ---

func findCSRFToken(n *html.Node) (string, bool) {
	if n.Type == html.ElementNode && n.Data == "input" && attr(n, "name") == "_csrf" {
		return attr(n, "value"), true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v, ok := findCSRFToken(c); ok {
			return v, true
		}
	}
	return "", false
}

// findFirstOAuth2SourceID walks the admin auth-sources table and returns the
// ID of the first row whose Type column reads "OAuth2".
func findFirstOAuth2SourceID(n *html.Node) (int, bool) {
	tbody := findElement(n, "tbody")
	if tbody == nil {
		return 0, false
	}
	for row := tbody.FirstChild; row != nil; row = row.NextSibling {
		if row.Type != html.ElementNode || row.Data != "tr" {
			continue
		}
		var cells []string
		for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type == html.ElementNode && cell.Data == "td" {
				cells = append(cells, strings.TrimSpace(textContent(cell)))
			}
		}
		if len(cells) >= 3 && cells[2] == "OAuth2" {
			id, err := strconv.Atoi(cells[0])
			if err == nil {
				return id, true
			}
		}
	}
	return 0, false
}

// findForm returns the <form method="post" action="{action}"> element, if any.
func findForm(n *html.Node, action string) (*html.Node, bool) {
	if n.Type == html.ElementNode && n.Data == "form" && attr(n, "action") == action {
		return n, true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f, ok := findForm(c, action); ok {
			return f, true
		}
	}
	return nil, false
}

// extractFormValues walks a <form> subtree and reproduces the values a
// browser would submit for it: text/hidden/password inputs by their value
// attribute, checked checkboxes/radios (defaulting to "on" per the HTML
// spec when no value attribute is set), unchecked ones omitted entirely,
// textareas by their text content, and selects by their selected (or first)
// option.
func extractFormValues(form *html.Node) url.Values {
	values := url.Values{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "input":
				name := attr(n, "name")
				if name == "" || strings.HasPrefix(name, "_autofill") {
					break
				}
				switch strings.ToLower(attr(n, "type")) {
				case "checkbox", "radio":
					if hasAttr(n, "checked") {
						v := attr(n, "value")
						if v == "" {
							v = "on"
						}
						values.Set(name, v)
					}
				case "submit", "button", "image", "reset", "file":
					// Not part of the state we need to preserve.
				default:
					values.Set(name, attr(n, "value"))
				}
			case "textarea":
				name := attr(n, "name")
				if name != "" {
					values.Set(name, textContent(n))
				}
			case "select":
				name := attr(n, "name")
				if name == "" {
					break
				}
				if v, ok := selectedOptionValue(n); ok {
					values.Set(name, v)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
	return values
}

func selectedOptionValue(sel *html.Node) (string, bool) {
	var first, selected *html.Node
	var find func(n *html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "option" {
			if first == nil {
				first = n
			}
			if hasAttr(n, "selected") {
				selected = n
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(sel)
	chosen := selected
	if chosen == nil {
		chosen = first
	}
	if chosen == nil {
		return "", false
	}
	if v := attr(chosen, "value"); v != "" {
		return v, true
	}
	return textContent(chosen), true
}

// findFlashError returns the text of Forgejo's flash-error banner, if present.
func findFlashError(n *html.Node) (string, bool) {
	el := findByClassToken(n, "flash-error")
	if el == nil {
		return "", false
	}
	msg := strings.TrimSpace(textContent(el))
	if msg == "" {
		return "", false
	}
	return msg, true
}

func findByClassToken(n *html.Node, class string) *html.Node {
	if n.Type == html.ElementNode && slices.Contains(strings.Fields(attr(n, "class")), class) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if el := findByClassToken(c, class); el != nil {
			return el
		}
	}
	return nil
}

func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if el := findElement(c, tag); el != nil {
			return el
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}
