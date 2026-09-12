package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMutationRequiresCSRFAndSameOrigin(t *testing.T) {
	flash := testFlash(t)
	s := &Server{cfg: Config{AppOrigin: "https://setup.example.com"}, flash: flash}
	token, err := flash.csrf("someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	request := func(site, origin, csrf string) *http.Request {
		form := url.Values{"csrf_token": {csrf}}
		r := httptest.NewRequest(http.MethodPost, "https://setup.example.com/setup-mcp/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	if err := s.validMutationRequest(request("same-origin", "", token), "someone@example.com"); err != nil {
		t.Fatalf("same-origin request failed: %v", err)
	}
	if err := s.validMutationRequest(request("cross-site", "https://setup.example.com", token), "someone@example.com"); err == nil {
		t.Fatal("cross-site Fetch Metadata was accepted")
	}
	if err := s.validMutationRequest(request("", "https://setup.example.com", token), "someone@example.com"); err != nil {
		t.Fatalf("origin fallback failed: %v", err)
	}
	if err := s.validMutationRequest(request("", "https://evil.example", token), "someone@example.com"); err == nil {
		t.Fatal("foreign Origin was accepted")
	}
	if err := s.validMutationRequest(request("same-origin", "", token+"x"), "someone@example.com"); err == nil {
		t.Fatal("bad CSRF signature was accepted")
	}
}

func TestFlashCookieHasRequiredBrowserControls(t *testing.T) {
	s := &Server{cfg: Config{BasePath: "/setup-mcp", FlashTTL: 5 * time.Minute}}
	w := httptest.NewRecorder()
	s.setFlashCookie(w, "sealed", 300)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/setup-mcp" || c.MaxAge != 300 {
		t.Fatalf("flash cookie = %#v", c)
	}
}

func TestSecurityHeadersCoverHealthResponses(t *testing.T) {
	s := &Server{cfg: Config{BasePath: "/setup-mcp"}, metrics: newMetrics()}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy", "Cross-Origin-Opener-Policy"} {
		if w.Header().Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
}
