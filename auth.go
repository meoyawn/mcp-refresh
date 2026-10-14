package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

// Use a browser-compatible user agent for imported browser sessions.
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36"

func importSession(ctx context.Context, browserName, profile, accountID string, httpClient *http.Client) (*session, error) {
	if profile != "" && browserName == "" {
		return nil, fmt.Errorf("profile %q requires --browser", profile)
	}
	browsers := sweetcookie.DefaultBrowsers()
	if browserName != "" {
		b, err := parseBrowser(browserName)
		if err != nil {
			return nil, err
		}
		browsers = []sweetcookie.Browser{b}
	}
	var failures []string
	for _, browser := range browsers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := sweetcookie.Get(ctx, sweetcookie.Options{
			URL: origin + "/api/auth/session", Origins: []string{origin + "/backend-api/"},
			Browsers: []sweetcookie.Browser{browser}, Mode: sweetcookie.ModeMerge,
			Profiles: map[sweetcookie.Browser]string{browser: profile}, Timeout: 10 * time.Second,
		})
		if err != nil {
			failures = append(failures, string(browser)+": cookie import failed")
			continue
		}
		// Keep profiles and Firefox containers separate: never combine two logins.
		groups := map[string][]*http.Cookie{}
		var order []string
		profiles := map[string]string{}
		for _, c := range result.Cookies {
			key := fmt.Sprintf("%s\x00%s\x00%d", c.Source.StorePath, c.Source.Profile, c.Container.ID)
			if _, ok := groups[key]; !ok {
				order = append(order, key)
				profiles[key] = c.Source.Profile
			}
			cookie := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
			if c.Expires != nil {
				cookie.Expires = *c.Expires
			}
			if cookie.Valid() != nil {
				continue
			}
			groups[key] = append(groups[key], cookie)
		}
		for _, key := range order {
			if !hasSessionCookie(groups[key]) {
				continue
			}
			s := &session{Version: 1, Browser: string(browser), Profile: profiles[key], AccountID: accountID, UserAgent: defaultUserAgent, ImportedAt: time.Now().UTC(), Cookies: groups[key]}
			if profile != "" {
				s.Profile = profile
			}
			client, err := newAPIClient(httpClient, s)
			if err == nil {
				err = client.authenticate(ctx)
			}
			if err == nil {
				return s, nil
			}
			failures = append(failures, fmt.Sprintf("%s profile %q: %v", browser, profiles[key], err))
		}
		if len(order) == 0 && len(result.Warnings) > 0 {
			// Browser warnings may contain raw database values; do not print them.
			failures = append(failures, string(browser)+": no readable ChatGPT cookies (browser storage or keychain unavailable)")
		}
	}
	msg := "no usable ChatGPT session found; sign in at chatgpt.com, then run mcp-refresh auth import --browser <browser> [--profile <profile or Cookies database>]"
	if len(failures) > 0 {
		msg += "\n" + strings.Join(failures, "\n")
	}
	return nil, fmt.Errorf("%s", msg)
}

func parseBrowser(name string) (sweetcookie.Browser, error) {
	b := sweetcookie.Browser(strings.ToLower(name))
	switch b {
	case sweetcookie.BrowserChrome, sweetcookie.BrowserChromium, sweetcookie.BrowserEdge,
		sweetcookie.BrowserBrave, sweetcookie.BrowserVivaldi, sweetcookie.BrowserOpera,
		sweetcookie.BrowserArc, sweetcookie.BrowserHelium, sweetcookie.BrowserDia,
		sweetcookie.BrowserComet, sweetcookie.BrowserAtlas, sweetcookie.BrowserWhale,
		sweetcookie.BrowserFirefox, sweetcookie.BrowserZen, sweetcookie.BrowserFloorp,
		sweetcookie.BrowserWaterfox, sweetcookie.BrowserLibreWolf, sweetcookie.BrowserSafari:
		return b, nil
	}
	return "", fmt.Errorf("unsupported browser %q; use chrome, chromium, edge, brave, vivaldi, opera, arc, helium, dia, comet, atlas, whale, firefox, zen, floorp, waterfox, librewolf, or safari", name)
}
