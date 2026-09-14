package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

type oidcTestProvider struct {
	server    *httptest.Server
	private   *rsa.PrivateKey
	idToken   string
	tokenFail bool
}

func newOIDCTestProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &oidcTestProvider{private: private}
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 provider.server.URL,
				"authorization_endpoint": provider.server.URL + "/authorize",
				"token_endpoint":         provider.server.URL + "/token",
				"jwks_uri":               provider.server.URL + "/keys",
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &provider.private.PublicKey, KeyID: "test", Algorithm: string(jose.RS256), Use: "sig"}}})
		case "/token":
			if provider.tokenFail {
				http.Error(w, "token exchange failed", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"token_type":   "Bearer",
				"id_token":     provider.idToken,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *oidcTestProvider) idTokenFor(t *testing.T, nonce string, expires time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.private}, (&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), "test"))
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"iss":    p.server.URL,
		"sub":    "subject",
		"aud":    "client",
		"exp":    expires.Unix(),
		"iat":    time.Now().Add(-time.Minute).Unix(),
		"nonce":  nonce,
		"email":  "allowed@example.com",
		"groups": []string{"/grafana-mcp"},
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	object, err := signer.Sign(encodedPayload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := object.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func nativeProviderConfig(provider *oidcTestProvider, key []byte) Config {
	return Config{
		AuthMode:         "native",
		BasePath:         "/setup-mcp",
		AppOrigin:        "https://app.example",
		GrafanaURL:       "http://grafana.example",
		GrafanaAPIMode:   grafanaAPIModeLegacy,
		GrafanaNamespace: "default",
		PublicURL:        "https://grafana.example",
		Issuer:           provider.server.URL,
		ClientID:         "client",
		OIDCClientSecret: "secret",
		OIDCScopes:       []string{"openid"},
		RequiredGroups:   []string{"/grafana-mcp"},
		FlashCookieKey:   key,
		FlashTTL:         5 * time.Minute,
		SessionCookieKey: key,
		SessionTTL:       time.Hour,
		RotationLockMode: "local",
	}
}

func nativeServerWithProvider(t *testing.T, provider *oidcTestProvider, key []byte) *Server {
	t.Helper()
	s, err := New(context.Background(), nativeProviderConfig(provider, key))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func nativeLoginState(t *testing.T, s *Server) (*http.Cookie, url.Values) {
	t.Helper()
	login := httptest.NewRecorder()
	s.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/setup-mcp/oauth2/login", nil))
	if login.Code != http.StatusFound {
		t.Fatalf("login status = %d", login.Code)
	}
	location, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %v", cookies)
	}
	return cookies[0], location.Query()
}

func nativeCallbackRequest(stateCookie *http.Cookie, state string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/setup-mcp/oauth2/callback?code=code&state="+url.QueryEscape(state), nil)
	request.AddCookie(stateCookie)
	return request
}

func bareNativeServer(t *testing.T) *Server {
	t.Helper()
	key := bytes.Repeat([]byte{4}, 32)
	session, err := newSessionCodec(key, "/setup-mcp")
	if err != nil {
		t.Fatal(err)
	}
	flash, err := newFlashCodec(key, 5*time.Minute, "/setup-mcp")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		cfg: Config{
			AuthMode:   "native",
			BasePath:   "/setup-mcp",
			AppOrigin:  "https://app.example",
			Issuer:     "https://id.example",
			ClientID:   "client",
			SessionTTL: time.Hour,
		},
		session: session,
		flash:   flash,
		oauth: &oauth2.Config{
			ClientID:    "client",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://id.example/authorize", TokenURL: "https://id.example/token"},
			RedirectURL: "https://app.example/setup-mcp/oauth2/callback",
			Scopes:      []string{"openid", "email"},
		},
		metrics: newMetrics(),
	}
}

func TestNativeLoginUsesFixedCallbackPKCEAndSecureStateCookie(t *testing.T) {
	s := bareNativeServer(t)
	req := httptest.NewRequest(http.MethodGet, "/setup-mcp/oauth2/login", nil)
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusFound {
		t.Fatalf("login status = %d", res.Code)
	}
	location, err := url.Parse(res.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	if query.Get("redirect_uri") != "https://app.example/setup-mcp/oauth2/callback" {
		t.Fatalf("redirect_uri = %q", query.Get("redirect_uri"))
	}
	if query.Get("code_challenge_method") != "S256" || len(query.Get("code_challenge")) != 43 {
		t.Fatalf("PKCE query = %v", query)
	}
	if query.Get("state") == "" || query.Get("nonce") == "" {
		t.Fatalf("OAuth query omitted state or nonce = %v", query)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != stateCookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/setup-mcp" {
		t.Fatalf("state cookie = %#v", cookies)
	}
}

func TestNativeUnauthenticatedGETRedirectsAndIgnoresProxyIdentity(t *testing.T) {
	s := bareNativeServer(t)
	request := httptest.NewRequest(http.MethodGet, "/setup-mcp", nil)
	request.Header.Set("X-Grafana-MCP-ID-Token", "proxy-token")
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, request)
	if res.Code != http.StatusFound || res.Header().Get("Location") != "/setup-mcp/oauth2/login" {
		t.Fatalf("native GET = %d %q", res.Code, res.Header().Get("Location"))
	}

	post := httptest.NewRequest(http.MethodPost, "/setup-mcp/token", strings.NewReader(""))
	post.Header.Set("X-Grafana-MCP-ID-Token", "proxy-token")
	post.Header.Set("Origin", "https://app.example")
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postResult := httptest.NewRecorder()
	s.Handler().ServeHTTP(postResult, post)
	if postResult.Code != http.StatusUnauthorized {
		t.Fatalf("native POST = %d, want 401", postResult.Code)
	}
}

func TestNativeCallbackRejectsWrongStateAndProviderError(t *testing.T) {
	s := bareNativeServer(t)
	login := httptest.NewRecorder()
	s.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/setup-mcp/oauth2/login", nil))
	stateCookie := login.Result().Cookies()[0]
	callback := httptest.NewRequest(http.MethodGet, "/setup-mcp/oauth2/callback?code=code&state=wrong", nil)
	callback.AddCookie(stateCookie)
	result := httptest.NewRecorder()
	s.Handler().ServeHTTP(result, callback)
	if result.Code != http.StatusBadRequest {
		t.Fatalf("wrong state status = %d", result.Code)
	}
	providerError := httptest.NewRequest(http.MethodGet, "/setup-mcp/oauth2/callback?error=access_denied", nil)
	providerResult := httptest.NewRecorder()
	s.Handler().ServeHTTP(providerResult, providerError)
	if providerResult.Code != http.StatusBadRequest {
		t.Fatalf("provider error status = %d", providerResult.Code)
	}
}

func TestNativeCallbackExchangesCodeChecksNonceAndSetsSession(t *testing.T) {
	provider := newOIDCTestProvider(t)
	s := nativeServerWithProvider(t, provider, bytes.Repeat([]byte{4}, 32))
	stateCookie, query := nativeLoginState(t, s)
	provider.idToken = provider.idTokenFor(t, query.Get("nonce"), time.Now().Add(time.Hour))
	callback := httptest.NewRecorder()
	s.Handler().ServeHTTP(callback, nativeCallbackRequest(stateCookie, query.Get("state")))
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/setup-mcp" {
		t.Fatalf("successful callback = %d %q", callback.Code, callback.Header().Get("Location"))
	}
	sessionCookies := callback.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, cookie := range sessionCookies {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v", sessionCookies)
	}
	request := httptest.NewRequest(http.MethodGet, "/setup-mcp", nil)
	request.AddCookie(sessionCookie)
	identity, err := s.nativeIdentity(request)
	if err != nil || identity != "allowed@example.com" {
		t.Fatalf("native identity = %q, %v", identity, err)
	}
}

func TestNativeCallbackRejectsWrongNonceAndExchangeFailure(t *testing.T) {
	provider := newOIDCTestProvider(t)
	s := nativeServerWithProvider(t, provider, bytes.Repeat([]byte{4}, 32))
	stateCookie, query := nativeLoginState(t, s)
	provider.idToken = provider.idTokenFor(t, "wrong-nonce", time.Now().Add(time.Hour))
	wrongNonce := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongNonce, nativeCallbackRequest(stateCookie, query.Get("state")))
	if wrongNonce.Code != http.StatusBadRequest {
		t.Fatalf("wrong nonce status = %d", wrongNonce.Code)
	}

	stateCookie, query = nativeLoginState(t, s)
	provider.tokenFail = true
	exchangeFailure := httptest.NewRecorder()
	s.Handler().ServeHTTP(exchangeFailure, nativeCallbackRequest(stateCookie, query.Get("state")))
	if exchangeFailure.Code != http.StatusBadRequest {
		t.Fatalf("exchange failure status = %d", exchangeFailure.Code)
	}
}

func TestNativeSessionUsesSameKeyAcrossServersAndRejectsOtherKey(t *testing.T) {
	provider := newOIDCTestProvider(t)
	key := bytes.Repeat([]byte{4}, 32)
	s := nativeServerWithProvider(t, provider, key)
	raw := provider.idTokenFor(t, "unused", time.Now().Add(time.Hour))
	sealed, err := s.session.sealSession(raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sameKey := nativeServerWithProvider(t, provider, key)
	request := httptest.NewRequest(http.MethodGet, "/setup-mcp", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sealed, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	identity, err := sameKey.nativeIdentity(request)
	if err != nil || identity != "allowed@example.com" {
		t.Fatalf("same-key identity = %q, %v", identity, err)
	}
	otherKey := nativeServerWithProvider(t, provider, bytes.Repeat([]byte{5}, 32))
	if _, err := otherKey.nativeIdentity(request); err == nil {
		t.Fatal("session opened with another pod key")
	}
}

func TestNativeIdentityRejectsExpiredIDTokenInsideLiveSession(t *testing.T) {
	provider := newOIDCTestProvider(t)
	s := nativeServerWithProvider(t, provider, bytes.Repeat([]byte{4}, 32))
	raw := provider.idTokenFor(t, "unused", time.Now().Add(-time.Minute))
	sealed, err := s.session.sealSession(raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/setup-mcp", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sealed, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	if _, err := s.nativeIdentity(request); err == nil {
		t.Fatal("expired ID token accepted by live session")
	}
}

func TestNativeLogoutRequiresCSRFAndClearsLocalCookies(t *testing.T) {
	provider := newOIDCTestProvider(t)
	s := nativeServerWithProvider(t, provider, bytes.Repeat([]byte{4}, 32))
	raw := provider.idTokenFor(t, "unused", time.Now().Add(time.Hour))
	sessionValue, err := s.session.sealSession(raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := s.flash.csrf("allowed@example.com")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, "/setup-mcp/oauth2/logout", strings.NewReader(form.Encode()))
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionValue, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	result := httptest.NewRecorder()
	s.Handler().ServeHTTP(result, request)
	if result.Code != http.StatusSeeOther || result.Header().Get("Location") != "/setup-mcp/oauth2/logged-out" {
		t.Fatalf("logout = %d %q", result.Code, result.Header().Get("Location"))
	}
	var deletedSession, deletedFlash bool
	for _, cookie := range result.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 {
			deletedSession = true
		}
		if cookie.Name == flashCookieName && cookie.MaxAge < 0 {
			deletedFlash = true
		}
	}
	if !deletedSession || !deletedFlash {
		t.Fatalf("logout did not clear local cookies = %v", result.Result().Cookies())
	}
}
