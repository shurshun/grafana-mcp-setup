// Package server issues personal, read-only Grafana tokens for MCP clients.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const maxFlashCookieValue = 3800

type authError struct {
	forbidden bool
	err       error
}

func (e *authError) Error() string { return e.err.Error() }

type readinessState struct {
	mu      sync.Mutex
	checked time.Time
	err     error
}

type requestIDKey struct{}

// Server authenticates users and manages their Grafana credentials.
type Server struct {
	cfg      Config
	grafana  *grafana
	verifier *oidc.IDTokenVerifier
	flash    *flashCodec
	locker   mutationLocker
	ready    readinessState
	metrics  *metrics
	oidcHTTP *http.Client
}

// New initializes authentication and rotation coordination.
func New(ctx context.Context, cfg Config) (*Server, error) {
	oidcHTTP := &http.Client{Timeout: 10 * time.Second}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, oidcHTTP), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering the OIDC provider at %s: %w", cfg.Issuer, err)
	}
	flash, err := newFlashCodec(cfg.FlashCookieKey, cfg.FlashTTL, cfg.BasePath)
	if err != nil {
		return nil, err
	}
	var locker mutationLocker = newLocalLocker()
	if cfg.RotationLockMode == "kubernetes" {
		locker, err = newLeaseLocker(cfg)
		if err != nil {
			return nil, err
		}
	}
	serverMetrics := newMetrics()
	return &Server{
		cfg: cfg,
		grafana: &grafana{
			base: cfg.GrafanaURL, token: cfg.GrafanaToken, mode: cfg.GrafanaAPIMode, namespace: cfg.GrafanaNamespace,
			hc: &http.Client{Timeout: 15 * time.Second}, metrics: serverMetrics,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		flash:    flash,
		locker:   locker,
		metrics:  serverMetrics,
		oidcHTTP: oidcHTTP,
	}, nil
}

// Handler returns the configured HTTP routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", s.metrics)
	mux.HandleFunc("GET "+s.cfg.BasePath, s.handlePage)
	mux.HandleFunc("GET "+s.cfg.BasePath+"/assets/{name}", s.handleAsset)
	mux.HandleFunc("POST "+s.cfg.BasePath+"/token", s.handleIssue)
	mux.HandleFunc("GET "+s.cfg.BasePath+"/token", s.handleShow)
	mux.HandleFunc("POST "+s.cfg.BasePath+"/revoke", s.handleRevoke)
	return s.securityHeaders(s.requestIDs(s.metrics.wrap(mux)))
}

func (s *Server) identity(r *http.Request) (string, error) {
	var raw string
	if s.cfg.IDTokenSource == "cookie" {
		c, err := r.Cookie(s.cfg.IDTokenCookie)
		if err != nil {
			s.recordAuth("oidc_failure")
			return "", &authError{err: errors.New("no ID token cookie")}
		}
		raw = c.Value
	} else {
		raw = strings.TrimSpace(r.Header.Get(s.cfg.IDTokenHeader))
		if raw == "" {
			s.recordAuth("oidc_failure")
			return "", &authError{err: errors.New("no ID token header")}
		}
	}
	tok, err := s.verifier.Verify(oidc.ClientContext(r.Context(), s.oidcHTTP), raw)
	if err != nil {
		s.recordAuth("oidc_failure")
		return "", &authError{err: errors.New("ID token verification failed")}
	}
	var claims struct {
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
	}
	if err := tok.Claims(&claims); err != nil {
		s.recordAuth("oidc_failure")
		return "", &authError{err: errors.New("ID token claims are invalid")}
	}
	return s.authorise(claims.Email, claims.Groups)
}

func (s *Server) authorise(email string, groups []string) (string, error) {
	if !validEmail(email) {
		return "", &authError{err: errors.New("the ID token carries no usable email")}
	}
	if len(s.cfg.RequiredGroups) > 0 && !slices.ContainsFunc(s.cfg.RequiredGroups, func(want string) bool {
		return slices.Contains(groups, want)
	}) {
		s.recordAuth("authorization_denial")
		return "", &authError{forbidden: true, err: errors.New("the identity lacks a required group")}
	}
	return strings.ToLower(email), nil
}

func validEmail(email string) bool {
	if len(email) == 0 || len(email) > 254 || strings.Count(email, "@") != 1 {
		return false
	}
	parts := strings.SplitN(email, "@", 2)
	if parts[0] == "" || parts[1] == "" {
		return false
	}
	return !strings.ContainsAny(email, " \t\r\n\x00")
}

func writeAuthError(w http.ResponseWriter, err error) {
	var ae *authError
	if errors.As(err, &ae) && ae.forbidden {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.Error(w, "not signed in", http.StatusUnauthorized)
}

func (s *Server) pageData(email string) (pageData, error) {
	csrf, err := s.flash.csrf(email)
	if err != nil {
		return pageData{}, err
	}
	return pageData{Email: email, TTLDays: s.ttlDays(), CSRFToken: csrf}, nil
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	email, err := s.identity(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	d, err := s.pageData(email)
	if err != nil {
		http.Error(w, "could not prepare the page", http.StatusInternalServerError)
		return
	}
	live, err := s.liveToken(r.Context(), email)
	if err != nil {
		s.audit(r.Context(), "status_failed", email, err)
		http.Error(w, "could not reach Grafana", http.StatusBadGateway)
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

func (s *Server) validMutationRequest(r *http.Request, email string) error {
	site := r.Header.Get("Sec-Fetch-Site")
	if site != "" {
		if site != "same-origin" {
			return errors.New("cross-origin request")
		}
	} else {
		origin := r.Header.Get("Origin")
		if origin == "" {
			if ref := r.Header.Get("Referer"); ref != "" {
				u, err := url.Parse(ref)
				if err == nil && u.Scheme != "" && u.Host != "" {
					origin = u.Scheme + "://" + u.Host
				}
			}
		}
		origin, err := normalizeOrigin(origin)
		if err != nil || origin != s.cfg.AppOrigin {
			return errors.New("request origin does not match APP_PUBLIC_URL")
		}
	}
	if err := r.ParseForm(); err != nil {
		return errors.New("invalid form")
	}
	return s.flash.validateCSRF(r.PostForm.Get("csrf_token"), email)
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	email, err := s.identity(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := s.validMutationRequest(r, email); err != nil {
		http.Error(w, "request verification failed", http.StatusForbidden)
		return
	}
	issued, err := s.prepareAndIssue(r.Context(), email)
	if err != nil {
		s.audit(r.Context(), "issue_failed", email, err, "service_account_id", issued.AccountID, "token_name", issued.TokenName)
		status := http.StatusBadGateway
		var preparation *deliveryPreparationError
		if errors.As(err, &preparation) {
			status = http.StatusInternalServerError
		}
		http.Error(w, "could not issue a token", status)
		return
	}
	s.setFlashCookie(w, issued.FlashCookie, int(s.cfg.FlashTTL.Seconds()))
	if issued.PartialCleanup {
		s.audit(r.Context(), "token_issued_partial_cleanup", email, nil, "service_account_id", issued.AccountID, "token_name", issued.TokenName)
	} else {
		s.audit(r.Context(), "token_issued", email, nil, "service_account_id", issued.AccountID, "token_name", issued.TokenName)
	}
	http.Redirect(w, r, s.cfg.BasePath+"/token", http.StatusSeeOther)
}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	email, err := s.identity(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	d, err := s.pageData(email)
	if err != nil {
		http.Error(w, "could not prepare the page", http.StatusInternalServerError)
		return
	}
	cookie, cookieErr := r.Cookie(flashCookieName)
	if cookieErr == nil {
		s.clearFlashCookie(w)
		if opened, err := s.flash.open(cookie.Value, email); err == nil {
			d.State = stateIssued
			d.Token = opened.Token
			d.PartialCleanup = opened.Partial
			d.fillFrom(saToken{Created: opened.TokenCreated, Expiration: opened.TokenExpires})
			s.render(w, d)
			return
		}
	}
	live, err := s.liveToken(r.Context(), email)
	if err != nil {
		s.audit(r.Context(), "status_failed", email, err)
		http.Error(w, "could not reach Grafana", http.StatusBadGateway)
		return
	}
	if live != nil {
		d.State = stateActive
		d.Spent = true
		d.fillFrom(*live)
	} else {
		d.State = stateNone
	}
	s.render(w, d)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	email, err := s.identity(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := s.validMutationRequest(r, email); err != nil {
		http.Error(w, "request verification failed", http.StatusForbidden)
		return
	}
	if err := s.revoke(r.Context(), email); err != nil {
		http.Error(w, "could not revoke every token", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, s.cfg.BasePath, http.StatusSeeOther)
}

func (s *Server) setFlashCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: flashCookieName, Value: value, Path: s.cfg.BasePath, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: time.Now().Add(s.cfg.FlashTTL)})
}

func (s *Server) clearFlashCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: flashCookieName, Path: s.cfg.BasePath, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Server) ttlDays() int { return int(s.cfg.TokenTTL.Hours() / 24) }

var errServiceAccountState = errors.New("service account must be an enabled Viewer")

func validateServiceAccount(sa *serviceAccount) error {
	if sa.Spec.Disabled {
		return fmt.Errorf("%w: disabled", errServiceAccountState)
	}
	if sa.Spec.Role != "Viewer" {
		return fmt.Errorf("%w: role %q", errServiceAccountState, sa.Spec.Role)
	}
	return nil
}

func (s *Server) liveToken(ctx context.Context, email string) (*saToken, error) {
	sa, err := s.grafana.findServiceAccount(ctx, "mcp-"+email)
	if err != nil || sa == nil {
		return nil, err
	}
	if err := validateServiceAccount(sa); err != nil {
		return nil, err
	}
	tokens, err := s.grafana.tokens(ctx, sa.Metadata.Name)
	if err != nil {
		return nil, err
	}
	var newest *saToken
	now := time.Now()
	for i := range tokens {
		t := tokens[i]
		if t.Revoked || t.HasExpired || (t.Expiration != nil && now.After(*t.Expiration)) {
			continue
		}
		if newest == nil || t.Created.After(newest.Created) {
			newest = &tokens[i]
		}
	}
	return newest, nil
}

type issueResult struct {
	Token          string
	FlashCookie    string
	PartialCleanup bool
	AccountID      string
	TokenName      string
}

type deliveryPreparationError struct{ err error }

func (e *deliveryPreparationError) Error() string {
	return "preparing secure token delivery: " + e.err.Error()
}

func (s *Server) prepareAndIssue(ctx context.Context, email string) (issueResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prepared, err := s.flash.prepareDelivery()
	if err != nil {
		s.recordAction("issue", "failure")
		return issueResult{}, &deliveryPreparationError{err: err}
	}
	return s.issue(ctx, email, prepared)
}

func (s *Server) issue(ctx context.Context, email string, prepared deliveryPreparation) (issued issueResult, err error) {
	action := "issue"
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		} else if issued.PartialCleanup {
			result = "partial"
		}
		s.recordAction(action, result)
	}()
	guard, err := s.locker.Acquire(ctx, email)
	if err != nil {
		return issueResult{}, err
	}
	defer guard.Release()

	title := "mcp-" + email
	sa, err := s.grafana.findServiceAccount(ctx, title)
	if err != nil {
		return issueResult{}, err
	}
	if sa == nil {
		if err := guard.Check(); err != nil {
			return issueResult{}, err
		}
		sa, err = s.grafana.createServiceAccount(ctx, title)
		if err != nil {
			return issueResult{}, err
		}
	} else {
		action = "reissue"
	}
	if err := validateServiceAccount(sa); err != nil {
		return issueResult{}, err
	}
	old, err := s.grafana.tokens(ctx, sa.Metadata.Name)
	if err != nil {
		return issueResult{}, err
	}
	entropy := make([]byte, 6)
	if _, err := rand.Read(entropy); err != nil {
		return issueResult{}, err
	}
	newName := tokenName(time.Now(), entropy)
	if err := guard.Check(); err != nil {
		return issueResult{}, err
	}
	issued.AccountID = sa.Metadata.Name
	issued.TokenName = newName
	created, err := s.grafana.newToken(ctx, sa.Metadata.Name, newName, s.cfg.TokenTTL)
	if err != nil {
		return issued, err
	}
	issued.Token = created.Secret
	successCookie := s.flash.sealPrepared(prepared.success, email, created.Secret, false, created.Created, created.Expires)
	partialCookie := s.flash.sealPrepared(prepared.partial, email, created.Secret, true, created.Created, created.Expires)
	if len(successCookie) > maxFlashCookieValue || len(partialCookie) > maxFlashCookieValue {
		if deleteErr := s.grafana.cleanupUndeliveredToken(sa.Metadata.Name, created.Name); deleteErr != nil {
			return issued, errors.Join(errors.New("replacement token exceeded the delivery cookie limit"), deleteErr)
		}
		return issued, errors.New("replacement token exceeded the delivery cookie limit")
	}
	issued.FlashCookie = successCookie
	for _, oldToken := range old {
		if err := guard.Check(); err != nil {
			issued.PartialCleanup = true
			issued.FlashCookie = partialCookie
			return issued, nil
		}
		if err := s.grafana.deleteToken(ctx, sa.Metadata.Name, oldToken.ID); err != nil {
			issued.PartialCleanup = true
			issued.FlashCookie = partialCookie
			return issued, nil
		}
	}
	return issued, nil
}

func (s *Server) revoke(ctx context.Context, email string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	deleted := 0
	attempted := 0
	accountID := ""
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
			if deleted > 0 {
				result = "partial"
			}
		}
		s.recordAction("revoke", result)
		event := "tokens_revoked"
		switch result {
		case "failure":
			event = "revoke_failed"
		case "partial":
			event = "tokens_revoked_partial"
		}
		s.audit(ctx, event, email, err, "service_account_id", accountID, "attempted", attempted, "deleted", deleted)
	}()
	guard, err := s.locker.Acquire(ctx, email)
	if err != nil {
		return err
	}
	defer guard.Release()
	sa, err := s.grafana.findServiceAccount(ctx, "mcp-"+email)
	if err != nil || sa == nil {
		return err
	}
	accountID = sa.Metadata.Name
	if sa.Spec.Role != "Viewer" {
		return fmt.Errorf("%w: refusing revoke for role %q", errServiceAccountState, sa.Spec.Role)
	}
	tokens, err := s.grafana.tokens(ctx, sa.Metadata.Name)
	if err != nil {
		return err
	}
	var errs []error
	for _, token := range tokens {
		if err := guard.Check(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		attempted++
		if err := s.grafana.deleteToken(ctx, sa.Metadata.Name, token.ID); err != nil {
			errs = append(errs, err)
		} else {
			deleted++
		}
	}
	return errors.Join(errs...)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	s.ready.mu.Lock()
	defer s.ready.mu.Unlock()
	if s.ready.checked.IsZero() || time.Since(s.ready.checked) >= s.cfg.ReadinessTTL {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		s.ready.err = s.grafana.ready(ctx)
		if s.ready.err == nil {
			if checker, ok := s.locker.(interface{ ready(context.Context) error }); ok {
				s.ready.err = checker.ready(ctx)
			}
		}
		cancel()
		s.ready.checked = time.Now()
	}
	if s.ready.err != nil {
		http.Error(w, s.ready.err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready\n"))
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		if r.URL.Path != "/metrics" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, c := range []byte(value) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func (s *Server) requestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				http.Error(w, "could not create request ID", http.StatusInternalServerError)
				return
			}
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func actorID(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(email)))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) audit(ctx context.Context, action, email string, err error, details ...any) {
	attrs := []any{"event", action, "issuer", s.cfg.Issuer, "actor", actorID(email), "request_id", ctx.Value(requestIDKey{})}
	if action == "token_issued_partial_cleanup" || action == "tokens_revoked_partial" {
		attrs = append(attrs, "result", "partial")
	} else if err != nil {
		attrs = append(attrs, "result", "failure")
	} else {
		attrs = append(attrs, "result", "success")
	}
	if err != nil {
		attrs = append(attrs, "reason", auditReason(err), "error_type", fmt.Sprintf("%T", err))
	} else if action == "token_issued_partial_cleanup" {
		attrs = append(attrs, "reason", "old_token_cleanup_incomplete")
	}
	attrs = append(attrs, details...)
	slog.InfoContext(ctx, "audit", attrs...)
}

func auditReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errServiceAccountState):
		return "invalid_service_account"
	case errors.Is(err, errTokenReconciliation):
		return "token_reconciliation_required"
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized:
			return "grafana_unauthorized"
		case http.StatusForbidden:
			return "grafana_forbidden"
		case http.StatusNotFound:
			return "grafana_not_found"
		case http.StatusConflict:
			return "grafana_conflict"
		default:
			return "grafana_api_error"
		}
	}
	var deliveryErr *deliveryPreparationError
	if errors.As(err, &deliveryErr) {
		return "delivery_preparation_failed"
	}
	return "operation_failed"
}

func (s *Server) recordAction(action, result string) {
	if s.metrics != nil {
		s.metrics.RecordAction(action, result)
	}
}

func (s *Server) recordAuth(kind string) {
	if s.metrics != nil {
		s.metrics.RecordAuth(kind)
	}
}

// SetVersion publishes the binary version in build metrics.
func (s *Server) SetVersion(version string) {
	if s.metrics != nil {
		s.metrics.SetVersion(version)
	}
}
