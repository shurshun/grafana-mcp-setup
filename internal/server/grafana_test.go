package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGrafana struct {
	mu         sync.Mutex
	accounts   map[string]*serviceAccount
	tokens     map[string][]tokenWire
	order      []string
	failDelete string
	secret     string
}

func account(title, uid, role string, disabled bool) *serviceAccount {
	sa := &serviceAccount{}
	sa.Metadata.Name = uid
	sa.Spec.Title = title
	sa.Spec.Role = role
	sa.Spec.Disabled = disabled
	return sa
}

func (f *fakeGrafana) handler(t *testing.T) http.Handler {
	t.Helper()
	collection := "/apis/iam.grafana.app/v0alpha1/namespaces/default/serviceaccounts"
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+collection, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		items := make([]*serviceAccount, 0, len(f.accounts))
		for _, sa := range f.accounts {
			items = append(items, sa)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"continue": ""}, "items": items})
	})
	mux.HandleFunc("POST "+collection, func(w http.ResponseWriter, r *http.Request) {
		var sa serviceAccount
		if err := json.NewDecoder(r.Body).Decode(&sa); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.order = append(f.order, "create-account")
		f.accounts[sa.Spec.Title] = &sa
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(sa)
	})
	mux.HandleFunc("DELETE "+collection+"/{sa}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uid := r.PathValue("sa")
		for title, sa := range f.accounts {
			if sa.Metadata.Name == uid {
				delete(f.accounts, title)
				delete(f.tokens, uid)
				f.order = append(f.order, "delete-account:"+uid)
				break
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET "+collection+"/{sa}/tokens", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		items := append([]tokenWire(nil), f.tokens[r.PathValue("sa")]...)
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "continue": ""})
	})
	mux.HandleFunc("POST "+collection+"/{sa}/tokens", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TokenName string `json:"tokenName"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.order = append(f.order, "create:"+body.TokenName)
		f.tokens[r.PathValue("sa")] = append(f.tokens[r.PathValue("sa")], tokenWire{Title: body.TokenName, Created: time.Now().Unix(), Expires: time.Now().Add(time.Hour).Unix()})
		w.WriteHeader(http.StatusCreated)
		secret := f.secret
		if secret == "" {
			secret = "glsa_new"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": secret, "serviceAccountTokenName": body.TokenName, "expires": time.Now().Add(time.Hour).Unix()})
	})
	mux.HandleFunc("DELETE "+collection+"/{sa}/tokens/{token}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("token")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.order = append(f.order, "delete:"+name)
		if name == f.failDelete {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		items := f.tokens[r.PathValue("sa")]
		for i := range items {
			if items[i].Title == name {
				f.tokens[r.PathValue("sa")] = append(items[:i], items[i+1:]...)
				break
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
	})
	return mux
}

func testServer(t *testing.T, f *fakeGrafana) *Server {
	t.Helper()
	ts := httptest.NewServer(f.handler(t))
	t.Cleanup(ts.Close)
	return &Server{
		cfg:     Config{TokenTTL: 90 * 24 * time.Hour},
		grafana: &grafana{base: ts.URL, token: "admin", mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()},
		locker:  newLocalLocker(),
		flash:   testFlash(t),
	}
}

func cleanupConfig(s *Server) Config {
	return Config{GrafanaURL: s.grafana.base, GrafanaToken: "admin", GrafanaAPIMode: grafanaAPIModeIAM, GrafanaNamespace: "default", RotationLockMode: "local"}
}

func issueForTest(t *testing.T, s *Server, email string) (issueResult, error) {
	t.Helper()
	prepared, err := s.flash.prepareDelivery()
	if err != nil {
		return issueResult{}, err
	}
	return s.issue(context.Background(), email, prepared)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestIssueCreatesViewerAccount(t *testing.T) {
	f := &fakeGrafana{accounts: map[string]*serviceAccount{}, tokens: map[string][]tokenWire{}}
	s := testServer(t, f)
	got, err := issueForTest(t, s, "someone@example.com")
	if err != nil || got.Token != "glsa_new" || got.PartialCleanup {
		t.Fatalf("issue = %#v, %v", got, err)
	}
	sa := f.accounts["mcp-someone@example.com"]
	if sa == nil || sa.Spec.Role != "Viewer" || sa.Spec.Disabled {
		t.Fatalf("created account = %#v", sa)
	}
}

func TestRotationCreatesBeforeDeleting(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{
		accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens:   map[string][]tokenWire{uid: {{Title: "old", Created: time.Now().Add(-time.Hour).Unix()}}},
	}
	s := testServer(t, f)
	if _, err := issueForTest(t, s, "someone@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(f.order) < 2 || !strings.HasPrefix(f.order[0], "create:mcp-") || f.order[1] != "delete:old" {
		t.Fatalf("mutation order = %v", f.order)
	}
}

func TestIssueRefusesWrongRoleAndDisabledAccounts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     string
		disabled bool
	}{{"wrong role", "Admin", false}, {"disabled", "Viewer", true}} {
		t.Run(tc.name, func(t *testing.T) {
			title := "mcp-someone@example.com"
			f := &fakeGrafana{accounts: map[string]*serviceAccount{title: account(title, "sa-one", tc.role, tc.disabled)}, tokens: map[string][]tokenWire{}}
			_, err := issueForTest(t, testServer(t, f), "someone@example.com")
			if err == nil || len(f.order) != 0 {
				t.Fatalf("issue error = %v, mutations = %v", err, f.order)
			}
		})
	}
}

func TestFailedOldTokenDeleteKeepsTheShowableReplacement(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{
		accounts:   map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens:     map[string][]tokenWire{uid: {{Title: "old"}}},
		failDelete: "old",
	}
	s := testServer(t, f)
	issued, err := issueForTest(t, s, "someone@example.com")
	if err != nil || !issued.PartialCleanup || issued.Token != "glsa_new" {
		t.Fatalf("partial rotation = %#v, %v", issued, err)
	}
	opened, err := s.flash.open(issued.FlashCookie, "someone@example.com")
	if err != nil || !opened.Partial || opened.Token != "glsa_new" {
		t.Fatalf("partial delivery cookie = %#v, %v", opened, err)
	}
	if len(f.tokens[uid]) != 2 {
		t.Fatalf("replacement was discarded after ambiguous cleanup: %#v", f.tokens[uid])
	}
	if strings.HasPrefix(f.order[len(f.order)-1], "delete:mcp-") {
		t.Fatalf("replacement was blindly rolled back: %v", f.order)
	}
}

func TestDeliveryPreparationFailsBeforeAnyGrafanaMutation(t *testing.T) {
	f := &fakeGrafana{accounts: map[string]*serviceAccount{}, tokens: map[string][]tokenWire{}}
	s := testServer(t, f)
	s.flash.random = failingReader{}
	if _, err := s.prepareAndIssue(context.Background(), "someone@example.com"); err == nil {
		t.Fatal("delivery preparation unexpectedly succeeded")
	}
	if len(f.order) != 0 {
		t.Fatalf("Grafana mutated before delivery was prepared: %v", f.order)
	}
}

func TestOversizedDeliveryIsRevokedBeforeOldTokenCleanup(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{
		accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens:   map[string][]tokenWire{uid: {{Title: "old"}}},
		secret:   strings.Repeat("x", maxFlashCookieValue),
	}
	if _, err := issueForTest(t, testServer(t, f), "someone@example.com"); err == nil {
		t.Fatal("oversized cookie was accepted")
	}
	if len(f.tokens[uid]) != 1 || f.tokens[uid][0].Title != "old" {
		t.Fatalf("old credential was removed before delivery size validation: %#v", f.tokens[uid])
	}
	if len(f.order) != 2 || !strings.HasPrefix(f.order[0], "create:mcp-") || !strings.HasPrefix(f.order[1], "delete:mcp-") {
		t.Fatalf("unexpected oversized-token cleanup order: %v", f.order)
	}
}

func TestReadyExplainsDisabledIAMAPI(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	g := &grafana{base: ts.URL, token: "admin", mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()}
	err := g.ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kubernetesServiceAccountsApi") {
		t.Fatalf("ready error = %v", err)
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		t.Fatal("readiness leaked the raw 404 instead of the feature prerequisite")
	}
}

func TestGrafanaErrorDoesNotExposeResponseBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "glsa_secret_from_response", http.StatusBadRequest)
	}))
	defer ts.Close()
	g := &grafana{base: ts.URL, token: "admin", mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()}
	err := g.do(context.Background(), http.MethodPost, "/token", map[string]string{"safe": "request"}, nil)
	if err == nil || strings.Contains(err.Error(), "glsa_secret") {
		t.Fatalf("Grafana error = %v", err)
	}
}

func TestReconcileKeepsOnlyTheExplicitLiveToken(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	now := time.Now()
	f := &fakeGrafana{
		accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens: map[string][]tokenWire{uid: {
			{Title: "kept", Created: now.Unix(), Expires: now.Add(time.Hour).Unix()},
			{Title: "other", Created: now.Unix(), Expires: now.Add(time.Hour).Unix()},
		}},
	}
	s := testServer(t, f)
	if err := Reconcile(context.Background(), cleanupConfig(s), []string{"someone@example.com"}, CleanupOptions{Apply: true, KeepToken: "kept"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.tokens[uid]) != 1 || f.tokens[uid][0].Title != "kept" {
		t.Fatalf("tokens = %#v", f.tokens[uid])
	}
}

func TestReconcileValidatesKeepTokenBeforeDeleting(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{
		accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens:   map[string][]tokenWire{uid: {{Title: "only-live", Expires: time.Now().Add(time.Hour).Unix()}}},
	}
	s := testServer(t, f)
	if err := Reconcile(context.Background(), cleanupConfig(s), []string{"someone@example.com"}, CleanupOptions{Apply: true, KeepToken: "missing"}, io.Discard); err == nil {
		t.Fatal("missing keep token was accepted")
	}
	if len(f.order) != 0 {
		t.Fatalf("tokens changed before keep-token validation: %v", f.order)
	}
}

func TestOffboardRefusesAdminAccount(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{accounts: map[string]*serviceAccount{title: account(title, uid, "Admin", false)}, tokens: map[string][]tokenWire{}}
	s := testServer(t, f)
	if err := Offboard(context.Background(), cleanupConfig(s), []string{"someone@example.com"}, true, io.Discard); err == nil {
		t.Fatal("Admin account was accepted for deletion")
	}
	if len(f.order) != 0 {
		t.Fatalf("Admin account changed: %v", f.order)
	}
}

func TestReadyRequiresTokenAPIAndAuthorizedCollection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tokenVerbs []string
		collection int
		wantReady  bool
	}{
		{"missing token API", nil, http.StatusOK, false},
		{"missing token delete", []string{"get", "create"}, http.StatusOK, false},
		{"invalid credentials", []string{"get", "create", "delete"}, http.StatusUnauthorized, false},
		{"ready", []string{"get", "create", "delete"}, http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/apis/"+iamAPIVersion {
					resources := []map[string]any{{"name": "serviceaccounts", "verbs": []string{"list", "create", "delete"}}}
					if tc.tokenVerbs != nil {
						resources = append(resources, map[string]any{"name": "serviceaccounts/tokens", "verbs": tc.tokenVerbs})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"resources": resources})
					return
				}
				w.WriteHeader(tc.collection)
				_, _ = io.WriteString(w, `{"items":[]}`)
			}))
			defer ts.Close()
			g := &grafana{base: ts.URL, mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()}
			if err := g.ready(context.Background()); (err == nil) != tc.wantReady {
				t.Fatalf("ready returned %v; wantReady=%t", err, tc.wantReady)
			}
		})
	}
}

func TestAmbiguousTokenCreationCleansOnlyRequestedName(t *testing.T) {
	var mu sync.Mutex
	deleted := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
			return
		}
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleted = r.URL.Path
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer ts.Close()
	g := &grafana{base: ts.URL, mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := g.newToken(ctx, "sa-one", "new-unique-name", time.Hour); err == nil {
		t.Fatal("ambiguous creation unexpectedly succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasSuffix(deleted, "/sa-one/tokens/new-unique-name") {
		t.Fatalf("cleanup did not use an independent context and exact requested name: %q", deleted)
	}
}

func TestTokenCreationConflictDoesNotDeleteExistingToken(t *testing.T) {
	deleted := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer ts.Close()
	g := &grafana{base: ts.URL, mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()}
	if _, err := g.newToken(context.Background(), "sa-one", "existing", time.Hour); err == nil {
		t.Fatal("conflict unexpectedly succeeded")
	}
	if deleted {
		t.Fatal("cleanup deleted a pre-existing name after conflict")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestRotationDeadlineReturnsShowablePartialResult(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{
		accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens:   map[string][]tokenWire{uid: {{Title: "old"}}},
	}
	s := testServer(t, f)
	transport := s.grafana.hc.Transport
	s.grafana.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/old") {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return transport.RoundTrip(r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	issued, err := s.prepareAndIssue(ctx, "someone@example.com")
	if err != nil || !issued.PartialCleanup || time.Since(started) > time.Second {
		t.Fatalf("deadline did not return a prompt partial result: %v", err)
	}
	opened, err := s.flash.open(issued.FlashCookie, "someone@example.com")
	if err != nil || opened.Token != "glsa_new" || !opened.Partial {
		t.Fatal("replacement was not deliverable after the cleanup deadline")
	}
}

func TestUnexpectedTokenNameAlwaysRequiresReconciliation(t *testing.T) {
	var deleted []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"token":"glsa_test","serviceAccountTokenName":"unrelated"}`)
			return
		}
		deleted = append(deleted, r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer ts.Close()
	g := &grafana{base: ts.URL, mode: grafanaAPIModeIAM, namespace: "default", hc: ts.Client()}
	_, err := g.newToken(context.Background(), "sa-one", "requested", time.Hour)
	if !errors.Is(err, errTokenReconciliation) {
		t.Fatal("unexpected name did not require reconciliation")
	}
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/requested") {
		t.Fatal("cleanup touched an unrelated token name")
	}
}

func TestOversizedTokenCleanupSurvivesRequestCancellation(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{
		accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)},
		tokens:   map[string][]tokenWire{uid: {{Title: "old"}}},
		secret:   strings.Repeat("x", maxFlashCookieValue),
	}
	s := testServer(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := s.grafana.hc.Transport
	s.grafana.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens") {
			recorder := httptest.NewRecorder()
			f.handler(t).ServeHTTP(recorder, r)
			cancel()
			return recorder.Result(), nil
		}
		return transport.RoundTrip(r)
	})
	issued, err := s.prepareAndIssue(ctx, "someone@example.com")
	if err == nil || issued.AccountID != uid || issued.TokenName == "" {
		t.Fatal("undeliverable token did not preserve failure audit identifiers")
	}
	if len(f.tokens[uid]) != 1 || f.tokens[uid][0].Title != "old" {
		t.Fatal("canceled delivery cleanup failed to preserve only the old token")
	}
}

func TestFailedCreationRetainsAttemptedAuditIdentifiers(t *testing.T) {
	title, uid := "mcp-someone@example.com", "sa-one"
	f := &fakeGrafana{accounts: map[string]*serviceAccount{title: account(title, uid, "Viewer", false)}, tokens: map[string][]tokenWire{uid: {{Title: "old"}}}}
	s := testServer(t, f)
	transport := s.grafana.hc.Transport
	s.grafana.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens") {
			recorder := httptest.NewRecorder()
			recorder.WriteHeader(http.StatusBadGateway)
			return recorder.Result(), nil
		}
		return transport.RoundTrip(r)
	})
	issued, err := s.prepareAndIssue(context.Background(), "someone@example.com")
	if err == nil || issued.AccountID != uid || !strings.HasPrefix(issued.TokenName, "mcp-") {
		t.Fatal("ambiguous creation discarded identifiers needed for recovery audit")
	}
	if issued.Token != "" || issued.FlashCookie != "" {
		t.Fatal("failed creation exposed token delivery state")
	}
}
