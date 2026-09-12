package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func legacyClient(ts *httptest.Server) *grafana {
	return &grafana{base: ts.URL, token: "admin", mode: grafanaAPIModeLegacy, hc: ts.Client()}
}

func TestLegacyReadyUsesOnlyTheLegacyAPI(t *testing.T) {
	requested := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.RequestURI()
		if r.URL.Path != legacyServiceAccountsPath+"/search" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"totalCount":0,"serviceAccounts":[]}`)
	}))
	defer ts.Close()
	if err := legacyClient(ts).ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requested != "/api/serviceaccounts/search?page=1&perpage=1" {
		t.Fatalf("readiness request = %q", requested)
	}
}

func TestLegacyFindServiceAccountPaginatesAndMatchesTheExactName(t *testing.T) {
	const title = "mcp-someone@example.com"
	pages := make([]int, 0, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != legacyServiceAccountsPath+"/search" || r.URL.Query().Get("query") != title || r.URL.Query().Get("perpage") != "100" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		if page == 1 {
			items := make([]legacyServiceAccount, 100)
			for i := range items {
				items[i] = legacyServiceAccount{ID: int64(i + 1), Name: fmt.Sprintf("prefix-%03d-%s", i, title), Role: "Viewer"}
			}
			_ = json.NewEncoder(w).Encode(legacySearchPage{TotalCount: 101, ServiceAccounts: items})
			return
		}
		_ = json.NewEncoder(w).Encode(legacySearchPage{TotalCount: 101, ServiceAccounts: []legacyServiceAccount{{ID: 501, Name: title, Role: "Viewer"}}})
	}))
	defer ts.Close()
	sa, err := legacyClient(ts).findServiceAccount(context.Background(), title)
	if err != nil {
		t.Fatal(err)
	}
	if sa == nil || sa.Metadata.Name != "501" || sa.Spec.Title != title || sa.Spec.Role != "Viewer" {
		t.Fatalf("service account = %#v", sa)
	}
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 {
		t.Fatalf("requested pages = %v", pages)
	}
}

func TestLegacyCreatePreservesViewerAndDisabledState(t *testing.T) {
	var request struct {
		Name       string `json:"name"`
		Role       string `json:"role"`
		IsDisabled bool   `json:"isDisabled"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != legacyServiceAccountsPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(legacyServiceAccount{ID: 42, Name: request.Name, Role: request.Role, IsDisabled: request.IsDisabled})
	}))
	defer ts.Close()
	sa, err := legacyClient(ts).createServiceAccount(context.Background(), "mcp-someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if request.Role != "Viewer" || request.IsDisabled || validateServiceAccount(sa) != nil || sa.Metadata.Name != "42" {
		t.Fatalf("request = %#v, service account = %#v", request, sa)
	}
	disabled, err := normalizeLegacyServiceAccount(legacyServiceAccount{ID: 43, Name: "mcp-disabled@example.com", Role: "Viewer", IsDisabled: true})
	if err != nil || validateServiceAccount(disabled) == nil {
		t.Fatalf("disabled service account validation = %v", err)
	}
	admin, err := normalizeLegacyServiceAccount(legacyServiceAccount{ID: 44, Name: "mcp-admin@example.com", Role: "Admin"})
	if err != nil || validateServiceAccount(admin) == nil {
		t.Fatalf("Admin service account validation = %v", err)
	}
}

func TestLegacyTokensNormalizeMetadataAndDeleteByNumericID(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	expires := created.Add(time.Hour)
	lastUsed := created.Add(time.Minute)
	deleted := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path
			_, _ = io.WriteString(w, `{"message":"API key deleted"}`)
			return
		}
		_ = json.NewEncoder(w).Encode([]legacyToken{
			{ID: 7, Name: "expired", Created: created, Expiration: &expires, LastUsedAt: &lastUsed, HasExpired: true},
			{ID: 8, Name: "live", Created: created},
		})
	}))
	defer ts.Close()
	g := legacyClient(ts)
	tokens, err := g.tokens(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].ID != "7" || tokens[0].Title != "expired" || !tokens[0].HasExpired || tokens[0].Revoked || tokens[0].Expiration == nil || tokens[0].LastUsedAt == nil {
		t.Fatalf("tokens = %#v", tokens)
	}
	if err := g.deleteToken(context.Background(), "42", tokens[1].ID); err != nil {
		t.Fatal(err)
	}
	if deleted != "/api/serviceaccounts/42/tokens/8" {
		t.Fatalf("delete path = %q", deleted)
	}
}

func TestLegacyTokenCreationUsesSecondsToLive(t *testing.T) {
	var request struct {
		Name          string `json:"name"`
		SecondsToLive int64  `json:"secondsToLive"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/serviceaccounts/42/tokens" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "name": request.Name, "key": "glsa_new"})
	}))
	defer ts.Close()
	issued, err := legacyClient(ts).newToken(context.Background(), "42", "requested", 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if request.Name != "requested" || request.SecondsToLive != 7776000 || issued.Secret != "glsa_new" || issued.Expires == nil {
		t.Fatalf("request = %#v, issued = %#v", request, issued)
	}
}

func TestLegacyAmbiguousCreationDeletesOnlyTheUniqueRequestedToken(t *testing.T) {
	var mu sync.Mutex
	deleted := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusBadGateway)
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]legacyToken{{ID: 77, Name: "requested"}, {ID: 78, Name: "keep"}})
		case http.MethodDelete:
			mu.Lock()
			deleted = r.URL.Path
			mu.Unlock()
			_, _ = io.WriteString(w, `{"message":"API key deleted"}`)
		}
	}))
	defer ts.Close()
	_, err := legacyClient(ts).newToken(context.Background(), "42", "requested", time.Hour)
	if err == nil {
		t.Fatal("ambiguous creation unexpectedly succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if deleted != "/api/serviceaccounts/42/tokens/77" {
		t.Fatalf("cleanup path = %q", deleted)
	}
}

func TestLegacyConflictDoesNotReconcileAnExistingName(t *testing.T) {
	requests := make([]string, 0, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusConflict)
	}))
	defer ts.Close()
	_, err := legacyClient(ts).newToken(context.Background(), "42", "existing", time.Hour)
	if err == nil || len(requests) != 1 || !strings.HasPrefix(requests[0], "POST ") {
		t.Fatalf("requests = %v, error = %v", requests, err)
	}
}

func TestLegacyReconcileKeepsAVisibleNameAndDeletesByNumericID(t *testing.T) {
	deleted := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == legacyServiceAccountsPath+"/search":
			_ = json.NewEncoder(w).Encode(legacySearchPage{TotalCount: 1, ServiceAccounts: []legacyServiceAccount{{ID: 42, Name: "mcp-someone@example.com", Role: "Viewer"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/serviceaccounts/42/tokens":
			_ = json.NewEncoder(w).Encode([]legacyToken{{ID: 7, Name: "kept"}, {ID: 8, Name: "remove"}})
		case r.Method == http.MethodDelete:
			deleted = r.URL.Path
			_, _ = io.WriteString(w, `{"message":"API key deleted"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	var output bytes.Buffer
	cfg := Config{GrafanaURL: ts.URL, GrafanaToken: "admin", GrafanaAPIMode: grafanaAPIModeLegacy, RotationLockMode: "local"}
	if err := Reconcile(context.Background(), cfg, []string{"someone@example.com"}, CleanupOptions{Apply: true, KeepToken: "kept"}, &output); err != nil {
		t.Fatal(err)
	}
	if deleted != "/api/serviceaccounts/42/tokens/8" || !strings.Contains(output.String(), "token=remove") {
		t.Fatalf("delete path = %q, output = %q", deleted, output.String())
	}
}

func TestReconcileHonorsGrafanaExpiredFlag(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, expiration := range []*time.Time{nil, &future} {
		for _, keepExpired := range []bool{true, false} {
			t.Run(fmt.Sprintf("expiration=%v/keep=%t", expiration, keepExpired), func(t *testing.T) {
				var deleted []string
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == legacyServiceAccountsPath+"/search":
						_ = json.NewEncoder(w).Encode(legacySearchPage{TotalCount: 1, ServiceAccounts: []legacyServiceAccount{{ID: 42, Name: "mcp-someone@example.com", Role: "Viewer"}}})
					case r.Method == http.MethodGet && r.URL.Path == "/api/serviceaccounts/42/tokens":
						_ = json.NewEncoder(w).Encode([]legacyToken{
							{ID: 7, Name: "expired", HasExpired: true, Expiration: expiration},
							{ID: 8, Name: "live", Expiration: &future},
						})
					case r.Method == http.MethodDelete:
						deleted = append(deleted, r.URL.Path)
						_, _ = io.WriteString(w, `{"message":"API key deleted"}`)
					default:
						http.NotFound(w, r)
					}
				}))
				defer ts.Close()
				cfg := Config{GrafanaURL: ts.URL, GrafanaToken: "admin", GrafanaAPIMode: grafanaAPIModeLegacy, RotationLockMode: "local"}
				opts := CleanupOptions{Apply: true, DeleteExpired: true}
				if keepExpired {
					opts = CleanupOptions{Apply: true, KeepToken: "expired"}
				}
				err := Reconcile(context.Background(), cfg, []string{"someone@example.com"}, opts, io.Discard)
				if keepExpired {
					if err == nil || len(deleted) != 0 {
						t.Fatalf("expired keep target must fail without deletion: err=%v deleted=%v", err, deleted)
					}
				} else if err != nil || len(deleted) != 1 || deleted[0] != "/api/serviceaccounts/42/tokens/7" {
					t.Fatalf("cleanup must delete only expired token: err=%v deleted=%v", err, deleted)
				}
			})
		}
	}
}
