package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestExpiredSessionReimportsBrowserCookies(t *testing.T) {
	// Exercise sweetcookie itself against a temporary Firefox cookie database.
	// No keychain, real browser profile, or mock cookie-import hook is needed.
	profile := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(profile, "cookies.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE moz_cookies (
		host TEXT, name TEXT, value TEXT, path TEXT, expiry INTEGER,
		isSecure INTEGER, isHttpOnly INTEGER, sameSite INTEGER, originAttributes TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO moz_cookies VALUES (?, ?, ?, '/', ?, 1, 1, 0, '')`,
		".chatgpt.com", "__Secure-next-auth.session-token", "fresh-browser-session", time.Now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var staleRequests, authenticatedLists atomic.Int32
	client := localHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/session":
			cookie, err := r.Cookie("__Secure-next-auth.session-token")
			if err != nil {
				t.Error("missing session cookie")
				w.WriteHeader(401)
				return
			}
			if cookie.Value == "stale-session" {
				staleRequests.Add(1)
				fmt.Fprint(w, `{}`) // ChatGPT may signal logout with a successful empty session.
				return
			}
			if cookie.Value != "fresh-browser-session" {
				t.Error("browser session was not reimported")
			}
			fmt.Fprint(w, `{"accessToken":"fixture-short-lived-bearer"}`)
		case listPath:
			if r.Header.Get("Authorization") != "Bearer fixture-short-lived-bearer" {
				t.Error("list did not use new session token")
			}
			authenticatedLists.Add(1)
			fmt.Fprint(w, `{"links":[]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	stored := testSession()
	stored.Profile = profile
	stored.Cookies = []*http.Cookie{{Name: "__Secure-next-auth.session-token", Value: "stale-session", Domain: "chatgpt.com", Path: "/", Secure: true}}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveSession(path, stored); err != nil {
		t.Fatal(err)
	}
	svc := &service{sessionPath: path, http: client, log: io.Discard}
	if _, err := svc.list(context.Background()); err != nil {
		t.Fatal(err)
	}
	if staleRequests.Load() != 1 || authenticatedLists.Load() != 1 {
		t.Error("expired session was not recovered before listing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fresh-browser-session") || strings.Contains(string(data), "stale-session") || strings.Contains(string(data), "fixture-short-lived-bearer") {
		t.Error("stored session must contain new browser cookies and no bearer token")
	}
}
