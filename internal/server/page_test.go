package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func render(t *testing.T, d pageData) string {
	t.Helper()
	s := &Server{cfg: Config{PublicURL: "https://grafana.example.com", BasePath: "/setup-mcp"}}
	w := httptest.NewRecorder()
	s.render(w, d)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: the page can carry a secret", got)
	}
	return w.Body.String()
}

// html/template escapes by context and will rewrite anything inside <script>
// that it cannot parse, so the rendered page is checked rather than assumed.
func TestIssuedPage(t *testing.T) {
	body := render(t, pageData{
		State:   stateIssued,
		Email:   "someone@example.com",
		Token:   "glsa_secret",
		TTLDays: 90,
	})

	for _, want := range []string{
		`class="tabs"`,
		`class="copy"`,
		`class="tok"`,
		"navigator.clipboard.writeText",
		"https://grafana.example.com",
		"--disable-write",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page is missing %q", want)
		}
	}
}

// On screen the token is asterisks; the real one rides along in the button's
// data attribute, which is what the copy handler puts on the clipboard.
func TestIssuedPageMasksTheToken(t *testing.T) {
	body := render(t, pageData{
		State:   stateIssued,
		Email:   "someone@example.com",
		Token:   "glsa_secret",
		TTLDays: 90,
	})

	mask := strings.Repeat("*", maskWidth)
	if !strings.Contains(body, `<span class="tok">`+mask+`</span>`) {
		t.Error("the snippet does not show the token as a run of asterisks")
	}
	if n := strings.Count(body, "glsa_secret"); n != 1 {
		t.Errorf("the token appears %d times, want 1 (the data attribute alone)", n)
	}
	if !strings.Contains(body, `data-token="glsa_secret"`) {
		t.Error("nothing on the page carries the token for the copy button")
	}
	for _, snippet := range snippetsOf(t, body) {
		if strings.Contains(snippet, "glsa_secret") {
			t.Error("a visible snippet leaks the token")
		}
	}
}

// Every client gets its own block, and the shapes differ: VS Code calls the map
// servers, Zed wraps the command, Codex writes TOML.
func TestIssuedPageCoversEveryClient(t *testing.T) {
	body := render(t, pageData{
		State:   stateIssued,
		Email:   "someone@example.com",
		Token:   "glsa_secret",
		TTLDays: 90,
	})

	for _, want := range []string{
		`data-format="claude-code"`,
		`data-format="claude-desktop"`,
		`data-format="codex"`,
		`data-format="cursor"`,
		`data-format="vscode"`,
		`data-format="zed"`,
		".mcp.json",
		"~/.codex/config.toml",
		".vscode/mcp.json",
		"[mcp_servers.grafana]",
		"context_servers",
		"&#34;servers&#34;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}

	// The first tab is the only panel open, so a reader without JavaScript sees
	// one config rather than six.
	if n := strings.Count(body, `class="panel" data-format=`); n != 6 {
		t.Errorf("%d panels, want one per client", n)
	}
	if n := strings.Count(body, " hidden>"); n != 5 {
		t.Errorf("%d panels start hidden, want all but the first", n)
	}
}

// The token placeholder has to survive highlighting: it is the one span the
// copy handler rewrites.
func TestEveryClientSnippetCarriesTheMask(t *testing.T) {
	body := render(t, pageData{
		State:   stateIssued,
		Email:   "someone@example.com",
		Token:   "glsa_secret",
		TTLDays: 90,
	})

	if n := strings.Count(body, `<span class="tok">`); n != 18 {
		t.Errorf("%d masked tokens, want one per client and launch mode", n)
	}
	if strings.Contains(body, tokenSentinel) {
		t.Error("the placeholder sentinel reached the page")
	}
}

// Someone who already holds a token lands here. The secret is gone for good, so
// the page reports the token rather than pretending it can show it.
func TestActivePageReportsTheTokenAndGuardsReissue(t *testing.T) {
	body := render(t, pageData{
		State:     stateActive,
		Email:     "someone@example.com",
		Created:   "1 Sep 2026",
		Expires:   "30 Nov 2026",
		ExpiresIn: "79",
		LastUsed:  "11 Sep 2026",
		TTLDays:   90,
	})

	for _, want := range []string{"1 Sep 2026", "30 Nov 2026", "79 days", "11 Sep 2026"} {
		if !strings.Contains(body, want) {
			t.Errorf("the status is missing %q", want)
		}
	}
	if strings.Contains(body, "data-token") {
		t.Error("the page offers a token to copy, but the secret is long gone")
	}
	if !strings.Contains(body, "&lt;your token&gt;") {
		t.Error("the snippet has no placeholder where the token goes")
	}

	// Reissue has to be a deliberate second click, not a stray one.
	if !strings.Contains(body, `id="confirm" hidden`) {
		t.Error("the reissue confirmation is not hidden behind a first click")
	}
	if !strings.Contains(body, `action="/setup-mcp/token"`) {
		t.Error("no reissue form")
	}
	if !strings.Contains(body, "<noscript>") {
		t.Error("without JavaScript the confirmation never opens and reissue is impossible")
	}
}

func TestSpentLinkSaysSoWithoutRotating(t *testing.T) {
	body := render(t, pageData{
		State:   stateActive,
		Spent:   true,
		Email:   "someone@example.com",
		Created: "1 Sep 2026",
		Expires: "30 Nov 2026",
	})

	if !strings.Contains(body, "already shown") {
		t.Error("a spent one-time link does not explain itself")
	}
}

func TestLandingPageOffersToIssue(t *testing.T) {
	body := render(t, pageData{State: stateNone, Email: "someone@example.com", TTLDays: 90, CSRFToken: "signed-csrf"})

	if !strings.Contains(body, `action="/setup-mcp/token"`) {
		t.Error("no issue form on the landing page")
	}
	if strings.Contains(body, "glsa_") {
		t.Error("the landing page rendered a token")
	}
	if strings.Contains(body, `id="snippet"`) {
		t.Error("the landing page shows a config for a token nobody has yet")
	}
	if !strings.Contains(body, `name="csrf_token" value="signed-csrf"`) {
		t.Error("issue form has no signed CSRF token")
	}
}

func TestPageUsesNonceCSPAndExplainsPartialCleanup(t *testing.T) {
	s := &Server{cfg: Config{PublicURL: "https://grafana.example.com", BasePath: "/setup-mcp"}}
	w := httptest.NewRecorder()
	s.render(w, pageData{State: stateIssued, Email: "someone@example.com", Token: "glsa_secret", PartialCleanup: true})
	body := w.Body.String()
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	if !strings.Contains(body, `script nonce="`) || !strings.Contains(body, `style nonce="`) {
		t.Fatal("inline resources have no CSP nonce")
	}
	if !strings.Contains(body, "did not confirm that every") {
		t.Fatal("partial cleanup warning is missing")
	}
	if !strings.Contains(body, "delete clients.dataset.token") {
		t.Fatal("successful clipboard copy does not clear the DOM token")
	}
}

func TestFillFromDescribesTheToken(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	expires := time.Now().Add(10 * 24 * time.Hour)

	var d pageData
	d.fillFrom(saToken{Created: created, Expiration: &expires})

	if d.Created != "1 Sep 2026" {
		t.Errorf("Created = %q", d.Created)
	}
	if d.ExpiresIn != "9" && d.ExpiresIn != "10" {
		t.Errorf("ExpiresIn = %q, want the days left", d.ExpiresIn)
	}
	if d.LastUsed != "never" {
		t.Errorf("LastUsed = %q, want never for a token nothing has touched", d.LastUsed)
	}
}

func snippetsOf(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, part := range strings.Split(body, "<pre>")[1:] {
		out = append(out, part[:strings.Index(part, "</pre>")])
	}
	if len(out) == 0 {
		t.Fatal("no snippet on the page")
	}
	return out
}
