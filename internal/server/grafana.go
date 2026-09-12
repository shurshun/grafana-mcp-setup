package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	iamAPIVersion        = "iam.grafana.app/v0alpha1"
	grafanaAPIModeIAM    = "iam"
	grafanaAPIModeLegacy = "legacy"
)

var errTokenReconciliation = errors.New("token cleanup requires reconciliation")

type grafana struct {
	base      string
	token     string
	mode      string
	namespace string
	hc        *http.Client
	metrics   *metrics
}

type apiError struct {
	Method string
	Path   string
	Status int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Grafana %s %s returned HTTP %d", e.Method, e.Path, e.Status)
}

type serviceAccount struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Metadata   struct {
		Name              string    `json:"name"`
		GenerateName      string    `json:"generateName,omitempty"`
		Namespace         string    `json:"namespace,omitempty"`
		CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Title    string `json:"title"`
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
		Plugin   string `json:"plugin"`
	} `json:"spec"`
}

type saToken struct {
	ID         string
	Title      string
	Revoked    bool
	Created    time.Time
	Expiration *time.Time
	LastUsedAt *time.Time
	HasExpired bool
}

type tokenWire struct {
	Title    string `json:"title"`
	Revoked  bool   `json:"revoked"`
	Expires  int64  `json:"expires"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	LastUsed int64  `json:"lastUsed"`
}

func (g *grafana) collectionPath() string {
	return "/apis/iam.grafana.app/v0alpha1/namespaces/" + url.PathEscape(g.namespace) + "/serviceaccounts"
}

func (g *grafana) do(ctx context.Context, method, path string, body, out any) error {
	started := time.Now()
	status := 0
	defer func() {
		if g.metrics != nil {
			g.metrics.RecordGrafana(method, status, time.Since(started))
		}
	}()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, rdr) // #nosec G704 -- base is trusted configuration.
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.hc.Do(req) // #nosec G704 -- base is trusted configuration.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return &apiError{Method: method, Path: path, Status: resp.StatusCode}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("decoding Grafana %s response: %w", path, err)
	}
	return nil
}

func (g *grafana) ready(ctx context.Context) error {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyReady(ctx)
	}
	var discovery struct {
		Resources []struct {
			Name  string   `json:"name"`
			Verbs []string `json:"verbs"`
		} `json:"resources"`
	}
	err := g.do(ctx, http.MethodGet, "/apis/"+iamAPIVersion, nil, &discovery)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return errors.New("grafana IAM API is unavailable; enable kubernetesServiceAccountsApi and kubernetesServiceAccountTokensApi")
	}
	if err != nil {
		return err
	}
	for name, verbs := range map[string][]string{
		"serviceaccounts":        {"list", "create", "delete"},
		"serviceaccounts/tokens": {"get", "create", "delete"},
	} {
		found := false
		for _, resource := range discovery.Resources {
			if resource.Name != name {
				continue
			}
			found = true
			for _, verb := range verbs {
				if !slices.Contains(resource.Verbs, verb) {
					return fmt.Errorf("grafana IAM resource %s lacks %s capability", name, verb)
				}
			}
		}
		if !found {
			return fmt.Errorf("grafana IAM resource %s is unavailable; enable kubernetesServiceAccountsApi and kubernetesServiceAccountTokensApi", name)
		}
	}
	return g.do(ctx, http.MethodGet, g.collectionPath()+"?limit=1", nil, &struct {
		Items []serviceAccount `json:"items"`
	}{})
}

func (g *grafana) findServiceAccount(ctx context.Context, title string) (*serviceAccount, error) {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyFindServiceAccount(ctx, title)
	}
	path := g.collectionPath()
	continuation := ""
	seen := make(map[string]bool)
	var found *serviceAccount
	for {
		q := url.Values{"limit": {"100"}}
		if continuation != "" {
			q.Set("continue", continuation)
		}
		var page struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []serviceAccount `json:"items"`
		}
		if err := g.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for i := range page.Items {
			if page.Items[i].Spec.Title == title {
				if found != nil {
					return nil, fmt.Errorf("multiple Grafana service accounts have title %q", title)
				}
				account := page.Items[i]
				found = &account
			}
		}
		if page.Metadata.Continue == "" {
			return found, nil
		}
		if seen[page.Metadata.Continue] {
			return nil, errors.New("grafana service account pagination repeated a continuation token")
		}
		seen[page.Metadata.Continue] = true
		continuation = page.Metadata.Continue
	}
}

func serviceAccountResourceName(title string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(title)))
	return "mcp-" + hex.EncodeToString(sum[:10])
}

func (g *grafana) createServiceAccount(ctx context.Context, title string) (*serviceAccount, error) {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyCreateServiceAccount(ctx, title)
	}
	body := serviceAccount{APIVersion: iamAPIVersion, Kind: "ServiceAccount"}
	body.Metadata.Name = serviceAccountResourceName(title)
	body.Metadata.Namespace = g.namespace
	body.Spec.Title = title
	body.Spec.Role = "Viewer"
	body.Spec.Disabled = false
	body.Spec.Plugin = ""
	var sa serviceAccount
	if err := g.do(ctx, http.MethodPost, g.collectionPath(), body, &sa); err != nil {
		return nil, err
	}
	return &sa, nil
}

func (g *grafana) deleteServiceAccount(ctx context.Context, name string) error {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyDeleteServiceAccount(ctx, name)
	}
	return g.do(ctx, http.MethodDelete, g.collectionPath()+"/"+url.PathEscape(name), nil, nil)
}

func unixTime(v int64) *time.Time {
	if v <= 0 {
		return nil
	}
	t := time.Unix(v, 0).UTC()
	return &t
}

func (g *grafana) tokens(ctx context.Context, saName string) ([]saToken, error) {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyTokens(ctx, saName)
	}
	base := g.collectionPath() + "/" + url.PathEscape(saName) + "/tokens"
	continuation := ""
	seen := make(map[string]bool)
	var all []saToken
	for {
		q := url.Values{"limit": {"100"}}
		if continuation != "" {
			q.Set("continue", continuation)
		}
		var page struct {
			Items    []tokenWire `json:"items"`
			Continue string      `json:"continue"`
		}
		if err := g.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, wire := range page.Items {
			expires := unixTime(wire.Expires)
			created := unixTime(wire.Created)
			lastUsed := unixTime(wire.LastUsed)
			t := saToken{ID: wire.Title, Title: wire.Title, Revoked: wire.Revoked, Expiration: expires, LastUsedAt: lastUsed}
			if created != nil {
				t.Created = *created
			}
			t.HasExpired = expires != nil && time.Now().After(*expires)
			all = append(all, t)
		}
		if page.Continue == "" {
			return all, nil
		}
		if seen[page.Continue] {
			return nil, errors.New("grafana token pagination repeated a continuation token")
		}
		seen[page.Continue] = true
		continuation = page.Continue
	}
}

func (g *grafana) deleteToken(ctx context.Context, saName, tokenID string) error {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyDeleteToken(ctx, saName, tokenID)
	}
	path := g.collectionPath() + "/" + url.PathEscape(saName) + "/tokens/" + url.PathEscape(tokenID)
	return g.do(ctx, http.MethodDelete, path, nil, &struct {
		Message string `json:"message"`
	}{})
}

type issuedToken struct {
	Secret  string
	Name    string
	Created time.Time
	Expires *time.Time
}

func (g *grafana) newToken(ctx context.Context, saName, tokenName string, ttl time.Duration) (issuedToken, error) {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyNewToken(ctx, saName, tokenName, ttl)
	}
	body := struct {
		TokenName        string `json:"tokenName"`
		ExpiresInSeconds int64  `json:"expiresInSeconds"`
	}{TokenName: tokenName, ExpiresInSeconds: int64(ttl.Seconds())}
	var out struct {
		Token                   string `json:"token"`
		ServiceAccountTokenName string `json:"serviceAccountTokenName"`
		Expires                 int64  `json:"expires"`
	}
	path := g.collectionPath() + "/" + url.PathEscape(saName) + "/tokens"
	if err := g.do(ctx, http.MethodPost, path, body, &out); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			return issuedToken{}, err
		}
		return issuedToken{}, errors.Join(err, g.cleanupUndeliveredToken(saName, tokenName))
	}
	if out.Token == "" {
		return issuedToken{}, errors.Join(errors.New("grafana returned an empty service account token"), g.cleanupUndeliveredToken(saName, tokenName))
	}
	if out.ServiceAccountTokenName != tokenName {
		return issuedToken{}, errors.Join(fmt.Errorf("%w: grafana returned an unexpected service account token name", errTokenReconciliation), g.cleanupUndeliveredToken(saName, tokenName))
	}
	return issuedToken{Secret: out.Token, Name: tokenName, Created: time.Now().UTC(), Expires: unixTime(out.Expires)}, nil
}

func (g *grafana) cleanupUndeliveredToken(saName, tokenName string) error {
	if g.mode != grafanaAPIModeIAM {
		return g.legacyCleanupUndeliveredToken(saName, tokenName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.deleteToken(ctx, saName, tokenName); err != nil {
		return fmt.Errorf("%w: %w", errTokenReconciliation, err)
	}
	return nil
}

func tokenName(now time.Time, entropy []byte) string {
	return "mcp-" + strconv.FormatInt(now.UTC().Unix(), 36) + "-" + hex.EncodeToString(entropy)
}
