package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CredResolver returns username/password for a registry host, and ok=false when
// none are configured (anonymous access).
type CredResolver func(registry string) (user, pass string, ok bool)

// tokenCache memoizes bearer tokens per (registry, scope) until they expire.
type tokenCache struct {
	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	token   string
	expires time.Time
}

func newTokenCache() *tokenCache {
	return &tokenCache{tokens: make(map[string]cachedToken)}
}

func (c *tokenCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[key]
	if !ok || time.Now().After(t.expires) {
		return "", false
	}
	return t.token, true
}

func (c *tokenCache) put(key, token string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	// Refresh a little early to avoid using a token right at expiry.
	c.tokens[key] = cachedToken{token: token, expires: time.Now().Add(ttl - 10*time.Second)}
}

// challenge is a parsed WWW-Authenticate: Bearer response.
type challenge struct {
	realm   string
	service string
	scope   string
}

// parseBearerChallenge parses a `Bearer realm="...",service="...",scope="..."`
// header value.
func parseBearerChallenge(header string) (challenge, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return challenge{}, false
	}
	var ch challenge
	for _, part := range splitParams(strings.TrimPrefix(header, "Bearer ")) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.TrimSpace(k) {
		case "realm":
			ch.realm = v
		case "service":
			ch.service = v
		case "scope":
			ch.scope = v
		}
	}
	return ch, ch.realm != ""
}

// splitParams splits a comma-separated parameter list, respecting quoted values
// (scopes can contain commas inside quotes, though rare).
func splitParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case ',':
			if inQuote {
				cur.WriteRune(r)
			} else {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// fetchToken performs the token request described by a bearer challenge, using
// basic-auth credentials when available.
func (c *Client) fetchToken(ch challenge, registry string) (string, time.Duration, error) {
	u, err := url.Parse(ch.realm)
	if err != nil {
		return "", 0, fmt.Errorf("bad realm %q: %w", ch.realm, err)
	}
	q := u.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	if ch.scope != "" {
		q.Set("scope", ch.scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, err
	}
	if c.creds != nil {
		if user, pass, ok := c.creds(registry); ok {
			req.SetBasicAuth(user, pass)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, fmt.Errorf("token request %s: %s", resp.Status, string(body))
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", 0, err
	}
	token := tr.Token
	if token == "" {
		token = tr.AccessToken
	}
	if token == "" {
		return "", 0, fmt.Errorf("empty token in response")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	return token, ttl, nil
}
