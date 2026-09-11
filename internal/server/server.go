// Package server issues personal, read-only Grafana tokens to whoever asks for
// one, so that people can point an MCP client at Grafana without an
// administrator minting tokens by hand.
//
// The split is identity from the user, privilege from the service. A reverse
// proxy in front of the base path runs the OIDC dance and leaves an ID token in
// a cookie; this service verifies that token and then acts with its own Admin
// token, because creating a service account needs Admin and Grafana OSS has no
// role between that and Viewer.
//
// Authenticating is not, by itself, permission. Whether it means anything
// depends on who can sign in to the identity provider, so set RequiredGroup
// unless you have decided that everyone who can sign in may hold a token.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Server serves the pages and owns the Grafana credentials behind them.
type Server struct {
	cfg      Config
	grafana  *grafana
	verifier *oidc.IDTokenVerifier
	stash    *stash
}

// New discovers the OIDC provider, which also proves it is reachable before the
// process starts serving.
func New(ctx context.Context, cfg Config) (*Server, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering the OIDC provider at %s: %w", cfg.Issuer, err)
	}

	return &Server{
		cfg:      cfg,
		grafana:  &grafana{base: cfg.GrafanaURL, token: cfg.GrafanaToken, hc: &http.Client{Timeout: 15 * time.Second}},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		stash:    newStash(),
	}, nil
}

// Handler routes everything under the configured base path, plus /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET "+s.cfg.BasePath, s.handlePage)
	mux.HandleFunc("POST "+s.cfg.BasePath+"/token", s.handleIssue)
	mux.HandleFunc("GET "+s.cfg.BasePath+"/token/{id}", s.handleShow)
	return mux
}

// identity verifies the ID token the proxy left in a cookie. The proxy already
// checked it, but the cookie reaches this process over plain HTTP inside the
// network, so the signature is checked again here rather than taken on trust.
func (s *Server) identity(r *http.Request) (string, error) {
	c, err := r.Cookie(s.cfg.IDTokenCookie)
	if err != nil {
		return "", errors.New("no ID token cookie")
	}
	tok, err := s.verifier.Verify(r.Context(), c.Value)
	if err != nil {
		return "", err
	}

	var claims struct {
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
	}
	if err := tok.Claims(&claims); err != nil {
		return "", err
	}
	return s.authorise(claims.Email, claims.Groups)
}

// authorise turns a verified token into the person it belongs to, or refuses.
// With RequiredGroups set, the identity provider decides who may hold a Grafana
// token, and Grafana's own access rules say nothing about it — the login in
// front of this service runs against the provider, not against Grafana.
func (s *Server) authorise(email string, groups []string) (string, error) {
	if email == "" {
		return "", errors.New("the ID token carries no email")
	}
	if len(s.cfg.RequiredGroups) > 0 && !slices.ContainsFunc(s.cfg.RequiredGroups, func(want string) bool {
		return slices.Contains(groups, want)
	}) {
		return "", fmt.Errorf("%q is in none of %s", email, strings.Join(s.cfg.RequiredGroups, ", "))
	}
	return strings.ToLower(email), nil
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	email, err := s.identity(r)
	if err != nil {
		slog.Warn("rejecting a request without a usable identity", "err", err)
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	d := pageData{Email: email, TTLDays: s.ttlDays()}
	live, err := s.liveToken(email)
	if err != nil {
		slog.Error("reading the token status", "email", email, "err", err)
		http.Error(w, "could not reach Grafana, see the service logs", http.StatusBadGateway)
		return
	}
	if live != nil {
		d.State = stateActive
		d.fillFrom(*live)
	} else {
		d.State = stateNone
	}
	s.render(w, d)
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	// A cross-site POST would carry a Lax OIDC cookie only on a top-level
	// navigation, but the origin check costs nothing and closes the gap.
	if o := r.Header.Get("Origin"); o != "" && o != s.cfg.PublicURL {
		http.Error(w, "bad origin", http.StatusForbidden)
		return
	}

	email, err := s.identity(r)
	if err != nil {
		slog.Warn("rejecting an issue request without a usable identity", "err", err)
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	token, err := s.issue(email)
	if err != nil {
		slog.Error("issuing a token", "email", email, "err", err)
		http.Error(w, "could not issue a token, see the service logs", http.StatusBadGateway)
		return
	}

	id, err := s.stash.put(email, token)
	if err != nil {
		slog.Error("stashing the new token", "email", email, "err", err)
		http.Error(w, "the token was issued but could not be shown; reissue it", http.StatusInternalServerError)
		return
	}
	// Post/redirect/get: the secret arrives through a GET, so reloading that
	// page cannot repeat the POST and rotate the token by accident.
	http.Redirect(w, r, s.cfg.BasePath+"/token/"+id, http.StatusSeeOther)
}

// handleShow spends the one-time id the issue redirect handed out. A reload
// finds nothing left and says so, which is the point: the secret is gone, but
// nothing was rotated behind the reader's back.
func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	email, err := s.identity(r)
	if err != nil {
		slog.Warn("rejecting a request without a usable identity", "err", err)
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	d := pageData{Email: email, TTLDays: s.ttlDays()}
	token, ok := s.stash.take(r.PathValue("id"), email)
	live, err := s.liveToken(email)
	if err != nil {
		slog.Error("reading the token status", "email", email, "err", err)
		http.Error(w, "could not reach Grafana, see the service logs", http.StatusBadGateway)
		return
	}
	if live != nil {
		d.fillFrom(*live)
	}

	switch {
	case ok:
		d.State = stateIssued
		d.Token = token
	case live != nil:
		d.State = stateActive
		d.Spent = true
	default:
		d.State = stateNone
	}
	s.render(w, d)
}

func (s *Server) ttlDays() int { return int(s.cfg.TokenTTL.Hours() / 24) }

// liveToken returns the newest token the account still has, or nil. Expired
// ones do not count: the page would offer to copy something Grafana rejects.
func (s *Server) liveToken(email string) (*saToken, error) {
	sa, err := s.grafana.findServiceAccount("mcp-" + email)
	if err != nil || sa == nil {
		return nil, err
	}
	tokens, err := s.grafana.tokens(sa.ID)
	if err != nil {
		return nil, err
	}

	var newest *saToken
	for i := range tokens {
		t := tokens[i]
		if t.HasExpired || (t.Expiration != nil && time.Now().After(*t.Expiration)) {
			continue
		}
		if newest == nil || t.Created.After(newest.Created) {
			newest = &tokens[i]
		}
	}
	return newest, nil
}

// issue creates or rotates the caller's service account token. The secret is
// returned and never stored: Grafana shows it once and so does this service.
func (s *Server) issue(email string) (string, error) {
	name := "mcp-" + email

	sa, err := s.grafana.findServiceAccount(name)
	if err != nil {
		return "", err
	}
	if sa == nil {
		if sa, err = s.grafana.createServiceAccount(name); err != nil {
			return "", err
		}
		slog.Info("created a service account", "name", name, "id", sa.ID)
	} else {
		dropped, err := s.grafana.dropTokens(sa.ID)
		if err != nil {
			return "", err
		}
		slog.Info("rotating", "name", name, "id", sa.ID, "revoked", dropped)
	}

	return s.grafana.newToken(sa.ID, "mcp", s.cfg.TokenTTL)
}
