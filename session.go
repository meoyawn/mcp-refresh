package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type session struct {
	Version    int            `json:"version"`
	Browser    string         `json:"browser"`
	Profile    string         `json:"profile"`
	AccountID  string         `json:"account_id,omitempty"`
	UserAgent  string         `json:"user_agent"`
	ImportedAt time.Time      `json:"imported_at"`
	Cookies    []*http.Cookie `json:"cookies"`
}

func loadSession(path string) (*session, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("open session %q: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat session: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("session %q must be a regular file", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("session %q is readable by other users; run chmod 600 on it", path)
	}
	var s session
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid session file %q; run mcp-refresh auth import", path)
	}
	if s.Version != 1 || !hasSessionCookie(s.Cookies) {
		return nil, fmt.Errorf("session file %q has no supported ChatGPT session; run mcp-refresh auth import", path)
	}
	for _, c := range s.Cookies {
		if c == nil || (strings.TrimPrefix(c.Domain, ".") != "chatgpt.com" && c.Domain != "") {
			return nil, fmt.Errorf("session file %q contains an invalid cookie domain", path)
		}
	}
	return &s, nil
}

func saveSession(path string, s *session) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return fmt.Errorf("create private session: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("protect session file: %w", err)
	}
	if err := json.NewEncoder(f).Encode(s); err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("flush session: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func hasSessionCookie(cookies []*http.Cookie) bool {
	for _, c := range cookies {
		if c == nil || c.Value == "" {
			continue
		}
		for _, prefix := range []string{"__Secure-next-auth.session-token", "__Secure-authjs.session-token", "next-auth.session-token", "authjs.session-token"} {
			if c.Name == prefix || strings.HasPrefix(c.Name, prefix+".") {
				return true
			}
		}
	}
	return false
}
