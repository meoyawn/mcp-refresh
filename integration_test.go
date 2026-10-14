package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Route the production HTTP client to a local server without adding a runtime
// base-URL override that could accidentally receive real browser credentials.
type localTransport struct {
	destination *url.URL
	transport   http.RoundTripper
}

func (r localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.destination.Scheme, r.destination.Host
	copy.URL = &u
	return r.transport.RoundTrip(copy)
}

func localHTTP(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	destination, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Transport = localTransport{destination, client.Transport}
	return client
}

func testSession() *session {
	return &session{Version: 1, Browser: "firefox", Profile: "fixture", UserAgent: "fixture-browser", ImportedAt: time.Now().UTC(), Cookies: []*http.Cookie{
		{Name: "__Secure-next-auth.session-token.0", Value: "fixture-session-0", Domain: "chatgpt.com", Path: "/", Secure: true, HttpOnly: true},
		{Name: "__Secure-next-auth.session-token.1", Value: "fixture-session-1", Domain: "chatgpt.com", Path: "/", Secure: true, HttpOnly: true},
	}}
}

func decodeBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	return body
}

func TestRefreshWithStoredBrowserSession(t *testing.T) {
	// Owns the CLI service-to-ChatGPT contract, including the captured payloads,
	// development filtering, exact name resolution, and cookie rotation on disk.
	var refreshes atomic.Int32
	var rejectRefresh atomic.Bool
	client := localHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("User-Agent") != "fixture-browser" {
			t.Error("missing browser user agent")
		}
		if _, err := r.Cookie("__Secure-next-auth.session-token.0"); err != nil {
			t.Error("missing stored chunked session")
		}
		if r.URL.Path == "/api/auth/session" {
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
				t.Error("session bootstrap must be a cookie-authenticated GET")
			}
			http.SetCookie(w, &http.Cookie{Name: "cf_clearance", Value: "rotated-clearance", Path: "/", Secure: true, MaxAge: 3600})
			fmt.Fprint(w, `{"accessToken":"fixture-bearer","account":{"id":"fixture-account"}}`)
			return
		}
		if r.Method != http.MethodPost {
			t.Error("backend request must be POST")
		}
		if r.Header.Get("Authorization") != "Bearer fixture-bearer" || r.Header.Get("ChatGPT-Account-Id") != "fixture-account" {
			t.Error("missing minted token or account context")
		}
		if r.Header.Get("Origin") != "https://chatgpt.com" {
			t.Error("missing ChatGPT origin")
		}
		switch r.URL.Path {
		case listPath:
			body := decodeBody(t, r)
			if !reflect.DeepEqual(body, map[string]any{"principals": []any{}, "link_refresh_strategy": "BLOCKING"}) {
				t.Errorf("unexpected list payload: %v", body)
			}
			fmt.Fprint(w, `{"links":[
			{"id":"link-example","name":"Primary","connector_id":"app-example","connector_name":"ExampleMCP","connector_type":"MCP","connector_status":"ONLY_ME","connector_distribution_channel":"INDIVIDUAL","auth_status":"ACTIVE","actions":["old"]},
			{"id":"link-other","name":"Primary","connector_id":"app-other","connector_name":"Other MCP","connector_type":"MCP","connector_status":"ONLY_ME","connector_distribution_channel":"INDIVIDUAL","auth_status":"ACTIVE","actions":[]},
			{"id":"link-public","name":"Published","connector_id":"app-public","connector_name":"Published","connector_type":"MCP","connector_status":"ENABLED","connector_distribution_channel":"ECOSYSTEM_DIRECTORY","actions":[]},
			{"id":"link-service","name":"Service","connector_id":"app-service","connector_type":"SERVICE","connector_status":"ONLY_ME","connector_distribution_channel":"INDIVIDUAL","actions":[]}
			]}`)
		case refreshPath:
			refreshes.Add(1)
			if body := decodeBody(t, r); !reflect.DeepEqual(body, map[string]any{"link_id": "link-example"}) {
				t.Errorf("refresh must use the link ID, got %v", body)
			}
			if rejectRefresh.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"secret":"never echo server errors"}`)
				return
			}
			fmt.Fprint(w, `{"actions":[{"name":"get_status","description":"untrusted remote instructions"},{"name":"list_items"}]}`)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	path := filepath.Join(t.TempDir(), "config", "session.json")
	if err := saveSession(path, testSession()); err != nil {
		t.Fatal(err)
	}
	svc := &service{sessionPath: path, http: client, log: io.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listed, err := svc.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected only two development MCPs, got %+v", listed)
	}
	result, err := svc.refresh(ctx, "examplemcp")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Name != "ExampleMCP" || result.LinkID != "link-example" || !reflect.DeepEqual(result.Actions, []string{"get_status", "list_items"}) {
		t.Fatalf("unexpected refresh result: %+v", result)
	}
	stored, err := loadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	var rotated bool
	for _, c := range stored.Cookies {
		if c.Name == "cf_clearance" && c.Value == "rotated-clearance" {
			rotated = true
			if c.Expires.IsZero() || c.MaxAge != 0 {
				t.Error("persisted cookie must retain an absolute expiry")
			}
		}
	}
	if !rotated {
		t.Error("rotated browser cookie was not persisted")
	}
	for _, test := range []struct{ name, message string }{
		{"Primary", "ambiguous"}, {"Example", "not found"}, {"link-public", "not found"}, {" ", "empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.refresh(ctx, test.name)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q error: %v", test.message, err)
			}
		})
	}
	if refreshes.Load() != 1 {
		t.Errorf("invalid selectors caused refresh calls: %d", refreshes.Load())
	}
	rejectRefresh.Store(true)
	_, err = svc.refresh(ctx, "link-example")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("backend rejection must be an error: %v", err)
	}
	if refreshes.Load() != 2 {
		t.Errorf("refresh POST was replayed after a 401")
	}
	if err != nil && strings.Contains(err.Error(), "never echo") {
		t.Error("backend error body leaked to caller")
	}
}

func TestHTTPRejectsRedirectsAndMalformedSuccess(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"redirect", 302, "", "HTTP 302"},
		{"rate limit", 429, `{"secret":"hidden"}`, "HTTP 429"},
		{"html challenge", 200, "<html>hidden</html>", "invalid JSON"},
		{"missing actions", 200, `{}`, "missing actions"},
		{"empty actions", 200, `{"actions":[]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := localHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://other.example/secret")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			})
			api, err := newAPIClient(client, testSession())
			if err != nil {
				t.Fatal(err)
			}
			_, err = api.refresh(context.Background(), link{ID: "link-fixture"})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "hidden") {
				t.Fatalf("expected sanitized %q error, got %v", test.want, err)
			}
			if calls.Load() != 1 {
				t.Error("request followed redirect or retried")
			}
		})
	}
}
