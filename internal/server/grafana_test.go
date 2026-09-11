package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeGrafana is enough of the service-account API to exercise the create and
// the rotate branch.
type fakeGrafana struct {
	accounts map[string]int    // name -> id
	tokens   map[int][]saToken // service account id -> its tokens
	deleted  []int
	nextID   int
}

func (f *fakeGrafana) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/serviceaccounts/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		out := struct {
			ServiceAccounts []serviceAccount `json:"serviceAccounts"`
		}{}
		if id, ok := f.accounts[q]; ok {
			out.ServiceAccounts = append(out.ServiceAccounts, serviceAccount{ID: id, Name: q, Role: "Viewer"})
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("POST /api/serviceaccounts", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			Role string `json:"role"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Role != "Viewer" {
			t.Errorf("service account created with role %q, want Viewer", body.Role)
		}
		f.nextID++
		f.accounts[body.Name] = f.nextID
		_ = json.NewEncoder(w).Encode(serviceAccount{ID: f.nextID, Name: body.Name, Role: body.Role})
	})

	mux.HandleFunc("GET /api/serviceaccounts/{id}/tokens", func(w http.ResponseWriter, r *http.Request) {
		out := f.tokens[atoi(t, r.PathValue("id"))]
		if out == nil {
			out = []saToken{}
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("DELETE /api/serviceaccounts/{id}/tokens/{tokenID}", func(_ http.ResponseWriter, r *http.Request) {
		f.deleted = append(f.deleted, atoi(t, r.PathValue("tokenID")))
	})

	mux.HandleFunc("POST /api/serviceaccounts/{id}/tokens", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SecondsToLive int `json:"secondsToLive"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.SecondsToLive <= 0 {
			t.Errorf("token requested with secondsToLive %d, want a positive TTL", body.SecondsToLive)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "glsa_fake"})
	})

	return mux
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func newServer(t *testing.T, f *fakeGrafana) *Server {
	t.Helper()
	ts := httptest.NewServer(f.handler(t))
	t.Cleanup(ts.Close)
	return &Server{
		cfg:     Config{TokenTTL: 90 * 24 * time.Hour},
		grafana: &grafana{base: ts.URL, token: "admin", hc: ts.Client()},
	}
}

func TestIssueCreatesViewerAccount(t *testing.T) {
	f := &fakeGrafana{accounts: map[string]int{}, tokens: map[int][]saToken{}}
	s := newServer(t, f)

	got, err := s.issue("someone@example.com")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if got != "glsa_fake" {
		t.Errorf("token = %q, want glsa_fake", got)
	}
	if _, ok := f.accounts["mcp-someone@example.com"]; !ok {
		t.Errorf("no service account created, have %v", f.accounts)
	}
}

func TestIssueRotatesExistingTokens(t *testing.T) {
	const name = "mcp-someone@example.com"
	f := &fakeGrafana{
		accounts: map[string]int{name: 7},
		tokens:   map[int][]saToken{7: {{ID: 11}, {ID: 12}}},
		nextID:   7,
	}
	s := newServer(t, f)

	if _, err := s.issue("someone@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(f.deleted) != 2 {
		t.Errorf("revoked %v, want both existing tokens", f.deleted)
	}
	if len(f.accounts) != 1 {
		t.Errorf("accounts = %v, want the existing one reused", f.accounts)
	}
}

// The landing page asks what the person already holds, so an expired token must
// not count: the page would otherwise show a config nobody can use.
func TestLiveTokenIgnoresExpiredOnes(t *testing.T) {
	const email = "someone@example.com"
	past := time.Now().Add(-24 * time.Hour)
	soon := time.Now().Add(30 * 24 * time.Hour)

	t.Run("no account", func(t *testing.T) {
		s := newServer(t, &fakeGrafana{accounts: map[string]int{}, tokens: map[int][]saToken{}})
		got, err := s.liveToken(email)
		if err != nil || got != nil {
			t.Fatalf("liveToken = %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("only expired", func(t *testing.T) {
		s := newServer(t, &fakeGrafana{
			accounts: map[string]int{"mcp-" + email: 7},
			tokens:   map[int][]saToken{7: {{ID: 11, Expiration: &past, HasExpired: true}}},
		})
		got, err := s.liveToken(email)
		if err != nil || got != nil {
			t.Fatalf("liveToken = %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("newest live one wins", func(t *testing.T) {
		older := saToken{ID: 11, Created: time.Now().Add(-72 * time.Hour), Expiration: &soon}
		newer := saToken{ID: 12, Created: time.Now().Add(-1 * time.Hour), Expiration: &soon}
		s := newServer(t, &fakeGrafana{
			accounts: map[string]int{"mcp-" + email: 7},
			tokens:   map[int][]saToken{7: {older, newer, {ID: 10, Expiration: &past, HasExpired: true}}},
		})
		got, err := s.liveToken(email)
		if err != nil {
			t.Fatalf("liveToken: %v", err)
		}
		if got == nil || got.ID != 12 {
			t.Fatalf("liveToken = %v, want the newest token (12)", got)
		}
	})
}
