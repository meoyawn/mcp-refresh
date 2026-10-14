package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

const origin = "https://chatgpt.com"
const listPath = "/backend-api/aip/connectors/links/list_accessible"
const refreshPath = "/backend-api/aip/connectors/mcp/refresh_actions"

type link struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ConnectorID     string   `json:"connector_id"`
	ConnectorName   string   `json:"connector_name"`
	ConnectorType   string   `json:"connector_type"`
	ConnectorStatus string   `json:"connector_status"`
	Distribution    string   `json:"connector_distribution_channel"`
	AuthStatus      string   `json:"auth_status"`
	Actions         []string `json:"actions"`
}

func (l link) displayName() string {
	if l.ConnectorName != "" {
		return l.ConnectorName
	}
	return l.Name
}

type refreshResult struct {
	Name        string   `json:"name"`
	LinkID      string   `json:"link_id"`
	ConnectorID string   `json:"connector_id"`
	Actions     []string `json:"actions"`
}

type apiClient struct {
	http    *http.Client
	session *session
	token   string
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func newAPIClient(httpClient *http.Client, s *session) (*apiClient, error) {
	if httpClient == nil || s == nil {
		return nil, fmt.Errorf("HTTP client and session must be non-nil")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	u, _ := url.Parse(origin)
	var validCookies []*http.Cookie
	for _, cookie := range s.Cookies {
		if cookie != nil && cookie.Valid() == nil {
			validCookies = append(validCookies, cookie)
		}
	}
	s.Cookies = validCookies
	jar.SetCookies(u, validCookies)
	owned := *httpClient
	owned.Jar = jar
	// Never follow an auth redirect with the browser's session or account headers.
	owned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &apiClient{http: &owned, session: s}, nil
}

func (c *apiClient) authenticate(ctx context.Context) error {
	var result struct {
		AccessToken string `json:"accessToken"`
		Account     struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/auth/session", nil, &result); err != nil {
		return err
	}
	if result.AccessToken == "" {
		return errSessionExpired
	}
	c.token = result.AccessToken
	if c.session.AccountID == "" {
		c.session.AccountID = result.Account.ID
	}
	return nil
}

func (c *apiClient) list(ctx context.Context) ([]link, error) {
	var result struct {
		Links []link `json:"links"`
	}
	// Field values and the empty principals array match the captured request.
	body := struct {
		Principals []string `json:"principals"`
		Strategy   string   `json:"link_refresh_strategy"`
	}{[]string{}, "BLOCKING"}
	if err := c.request(ctx, http.MethodPost, listPath, body, &result); err != nil {
		return nil, err
	}
	if result.Links == nil {
		return nil, fmt.Errorf("ChatGPT list response is missing links; the backend API may have changed")
	}
	links := make([]link, 0)
	for _, l := range result.Links {
		// Private individual MCPs are the development connections in the captures.
		if l.ConnectorType == "MCP" && l.ConnectorStatus == "ONLY_ME" && l.Distribution == "INDIVIDUAL" {
			if l.ID == "" || l.ConnectorID == "" {
				return nil, fmt.Errorf("ChatGPT returned a development MCP without its link or connector ID")
			}
			links = append(links, l)
		}
	}
	sort.Slice(links, func(i, j int) bool {
		return strings.ToLower(links[i].displayName()+links[i].ID) < strings.ToLower(links[j].displayName()+links[j].ID)
	})
	return links, nil
}

func resolveLink(links []link, name string) (link, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return link{}, fmt.Errorf("MCP name must not be empty")
	}
	var matches []link
	for _, l := range links {
		if strings.EqualFold(l.ID, name) || strings.EqualFold(l.ConnectorID, name) || strings.EqualFold(l.Name, name) || strings.EqualFold(l.ConnectorName, name) {
			matches = append(matches, l)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return link{}, fmt.Errorf("development MCP %q not found; run mcp-refresh list for exact names and IDs", name)
	}
	var choices []string
	for _, l := range matches {
		choices = append(choices, fmt.Sprintf("%s (%s)", l.displayName(), l.ID))
	}
	return link{}, fmt.Errorf("MCP %q is ambiguous: %s; use a link ID", name, strings.Join(choices, ", "))
}

func (c *apiClient) refresh(ctx context.Context, l link) (refreshResult, error) {
	var response struct {
		Actions []struct {
			Name string `json:"name"`
		} `json:"actions"`
	}
	body := struct {
		LinkID string `json:"link_id"`
	}{l.ID}
	if err := c.request(ctx, http.MethodPost, refreshPath, body, &response); err != nil {
		return refreshResult{}, err
	}
	if response.Actions == nil {
		return refreshResult{}, fmt.Errorf("ChatGPT refresh response is missing actions; refresh could not be confirmed")
	}
	result := refreshResult{Name: l.displayName(), LinkID: l.ID, ConnectorID: l.ConnectorID, Actions: make([]string, 0, len(response.Actions))}
	for _, a := range response.Actions {
		result.Actions = append(result.Actions, a.Name)
	}
	return result, nil
}

type apiError struct {
	status int
	path   string
}

func (e *apiError) Error() string {
	switch e.status {
	case 401:
		return fmt.Sprintf("ChatGPT %s: session expired (HTTP 401); run mcp-refresh auth import", e.path)
	case 403:
		return fmt.Sprintf("ChatGPT %s: access denied (HTTP 403); check your browser login and account/workspace, then run mcp-refresh auth import", e.path)
	case 429:
		return fmt.Sprintf("ChatGPT %s: rate limited (HTTP 429); try again later", e.path)
	default:
		return fmt.Sprintf("ChatGPT %s returned HTTP %d", e.path, e.status)
	}
}

func (c *apiClient) request(ctx context.Context, method, path string, body, result any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.session.UserAgent)
	req.Header.Set("Referer", origin+"/")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.session.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", c.session.AccountID)
	}
	req.Header.Set("OAI-Language", "en-US")
	req.Header.Set("OAI-Product-Sku", "CONNECTOR_SETTING")
	req.Header.Set("X-Codex-Surface", "settings")
	for _, cookie := range c.session.Cookies {
		if cookie != nil && cookie.Name == "oai-did" {
			req.Header.Set("oai-did", cookie.Value)
			break
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ChatGPT %s request failed: %w", path, err)
	}
	defer resp.Body.Close()
	c.rememberCookies(resp.Cookies(), req.URL)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{status: resp.StatusCode, path: path}
	}
	const maxResponse = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("read ChatGPT %s response: %w", path, err)
	}
	if len(data) > maxResponse {
		return fmt.Errorf("ChatGPT %s response exceeds 16 MiB", path)
	}
	if err := json.Unmarshal(data, result); err != nil {
		// Do not include response bodies or decoder errors that may contain secrets.
		return fmt.Errorf("ChatGPT %s returned invalid JSON (a browser challenge or API change may be responsible)", path)
	}
	return nil
}

func (c *apiClient) rememberCookies(cookies []*http.Cookie, requestURL *url.URL) {
	for _, cookie := range cookies {
		if cookie.Domain == "" {
			cookie.Domain = requestURL.Hostname()
		}
		if !strings.HasPrefix(cookie.Path, "/") {
			cookie.Path = "/"
			if lastSlash := strings.LastIndex(requestURL.Path, "/"); lastSlash > 0 {
				cookie.Path = requestURL.Path[:lastSlash]
			}
		}
		if strings.TrimPrefix(cookie.Domain, ".") != "chatgpt.com" {
			continue
		}
		kept := c.session.Cookies[:0]
		for _, old := range c.session.Cookies {
			if old.Name != cookie.Name || strings.TrimPrefix(old.Domain, ".") != strings.TrimPrefix(cookie.Domain, ".") || old.Path != cookie.Path {
				kept = append(kept, old)
			}
		}
		c.session.Cookies = kept
		if cookie.MaxAge >= 0 && (cookie.Expires.IsZero() || cookie.Expires.After(time.Now())) {
			if cookie.MaxAge > 0 {
				cookie.Expires = time.Now().Add(time.Duration(cookie.MaxAge) * time.Second).UTC()
				cookie.MaxAge = 0 // Persist an absolute expiry; loading must not extend it.
			}
			c.session.Cookies = append(c.session.Cookies, cookie)
		}
	}
}

var errSessionExpired = errors.New("ChatGPT session is signed out or expired; run mcp-refresh auth import")

func isUnauthorized(err error) bool {
	var apiErr *apiError
	return errors.Is(err, errSessionExpired) || (errors.As(err, &apiErr) && apiErr.status == http.StatusUnauthorized)
}
