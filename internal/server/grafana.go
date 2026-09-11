package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// grafana talks to the Grafana HTTP API as a service account with the Admin
// role. Creating service accounts needs Admin and Grafana OSS has no finer
// grain: RBAC is Enterprise-only.
type grafana struct {
	base  string
	token string
	hc    *http.Client
}

type serviceAccount struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (g *grafana) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	// The base URL is configuration and the path is a constant in this file;
	// nothing a request carries reaches either.
	req, err := http.NewRequest(method, g.base+path, rdr) // #nosec G704
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.hc.Do(req) // #nosec G704
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// findServiceAccount returns nil when nothing matches. Search is a substring
// match, so the exact name is compared again here.
func (g *grafana) findServiceAccount(name string) (*serviceAccount, error) {
	var page struct {
		ServiceAccounts []serviceAccount `json:"serviceAccounts"`
	}
	path := "/api/serviceaccounts/search?perpage=100&query=" + url.QueryEscape(name)
	if err := g.do(http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	for i := range page.ServiceAccounts {
		if page.ServiceAccounts[i].Name == name {
			return &page.ServiceAccounts[i], nil
		}
	}
	return nil, nil
}

func (g *grafana) createServiceAccount(name string) (*serviceAccount, error) {
	// Viewer regardless of the person's own role: MCP is read-only by policy.
	body := map[string]any{"name": name, "role": "Viewer", "isDisabled": false}
	var sa serviceAccount
	if err := g.do(http.MethodPost, "/api/serviceaccounts", body, &sa); err != nil {
		return nil, err
	}
	return &sa, nil
}

// saToken is what the API still knows about a token once it exists. The secret
// is not part of it: Grafana returns that only from the call that creates it.
type saToken struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Created    time.Time  `json:"created"`
	Expiration *time.Time `json:"expiration"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	HasExpired bool       `json:"hasExpired"`
}

func (g *grafana) tokens(saID int) ([]saToken, error) {
	var tokens []saToken
	path := fmt.Sprintf("/api/serviceaccounts/%d/tokens", saID)
	if err := g.do(http.MethodGet, path, nil, &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

// dropTokens removes every token the account already has, which is what makes a
// reissue a rotation rather than a pile of live credentials.
func (g *grafana) dropTokens(saID int) (int, error) {
	tokens, err := g.tokens(saID)
	if err != nil {
		return 0, err
	}
	path := fmt.Sprintf("/api/serviceaccounts/%d/tokens", saID)
	for _, t := range tokens {
		if err := g.do(http.MethodDelete, fmt.Sprintf("%s/%d", path, t.ID), nil, nil); err != nil {
			return 0, err
		}
	}
	return len(tokens), nil
}

// newToken returns the secret, which Grafana reveals exactly once.
func (g *grafana) newToken(saID int, name string, ttl time.Duration) (string, error) {
	body := map[string]any{"name": name, "secondsToLive": int(ttl.Seconds())}
	var out struct {
		Key string `json:"key"`
	}
	path := fmt.Sprintf("/api/serviceaccounts/%d/tokens", saID)
	if err := g.do(http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	return out.Key, nil
}
