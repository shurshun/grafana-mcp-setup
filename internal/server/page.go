package server

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Width of the asterisk run standing in for the token on screen. Fixed, so the
// page says nothing about the real token's length, and short enough that the
// snippet line does not need sideways scrolling.
const maskWidth = 12

// What the reader is looking at.
const (
	stateNone   = "none"   // no live token, so the page offers to issue one
	stateActive = "active" // a token exists, but its secret is long gone
	stateIssued = "issued" // this request is the one moment the secret exists
)

type pageData struct {
	Email          string
	BasePath       string
	Formats        []mcpFormat
	State          string
	Token          string
	Mask           string
	Spent          bool // the one-time link was already spent
	Created        string
	Expires        string
	ExpiresIn      string
	LastUsed       string
	TTLDays        int
	PublicURL      string
	CSRFToken      string
	CSPNonce       string
	StyleURL       string
	ScriptURL      string
	PartialCleanup bool
}

// fillFrom describes a token the API still knows about. Everything here is
// metadata; the secret itself Grafana hands over once and never again.
func (d *pageData) fillFrom(t saToken) {
	d.Created = t.Created.UTC().Format("2 Jan 2006")

	if t.Expiration != nil {
		d.Expires = t.Expiration.UTC().Format("2 Jan 2006")
		if days := int(time.Until(*t.Expiration).Hours() / 24); days >= 0 {
			d.ExpiresIn = strconv.Itoa(days)
		}
	} else {
		d.Expires = "never"
	}

	d.LastUsed = "never"
	if t.LastUsedAt != nil && !t.LastUsedAt.IsZero() {
		d.LastUsed = t.LastUsedAt.UTC().Format("2 Jan 2006")
	}
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Grafana MCP token</title>
<link rel="stylesheet" href="{{ .StyleURL }}">

<div class="shell">
  <header>
    <svg width="34" height="34" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <rect width="24" height="24" rx="7" fill="#f46800"/>
      <path d="M6 14.5l3.2-3.4 2.4 2.2L17 8" stroke="#fff" stroke-width="1.9"
            stroke-linecap="round" stroke-linejoin="round"/>
    </svg>
    <div>
      <h1>Grafana token for MCP</h1>
      <p class="sub">Read-only access for your MCP client</p>
    </div>
    <span class="chip">{{ .Email }}</span>
  </header>

{{ if eq .State "issued" }}
  <div class="card">
    <div class="callout">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
        <path d="M12 8v5M12 16.5v.5M10.3 3.9L2.6 17.4A1.8 1.8 0 004.2 20h15.6a1.8 1.8 0 001.6-2.6L13.7 3.9a1.9 1.9 0 00-3.4 0z"
              stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
	  <p>Copy it now. This response clears its short-lived delivery cookie. Do
	  not copy or share browser cookies; a stolen copy may be replayed for up to
	  five minutes. The Grafana token expires in {{ .TTLDays }} days.</p>
    </div>
	{{ if .PartialCleanup }}
	  <div class="callout">
		<p>The replacement token is ready, but Grafana did not confirm that every
		previous token was removed. Copy this token, then run the reconciliation
		command or revoke all tokens from this page.</p>
	  </div>
	{{ end }}

    <h2>Add this to your config</h2>
    {{ template "snippet" . }}

    <h2>What you are copying</h2>
    <p>The token is hidden on screen, and the button copies the real one. It is
    read-only: the service account behind it has the Viewer role, and
    <code>--disable-write</code> keeps the MCP server from offering write tools
    at all.</p>
  </div>

{{ else if eq .State "active" }}
  <div class="card">
    {{ if .Spent }}
      <div class="callout">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M12 8v5M12 16.5v.5M10.3 3.9L2.6 17.4A1.8 1.8 0 004.2 20h15.6a1.8 1.8 0 001.6-2.6L13.7 3.9a1.9 1.9 0 00-3.4 0z"
                stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <p>That token was already shown and cannot be shown again. If you did not
        copy it, issue a new one below.</p>
      </div>
    {{ end }}

    <h2>Your token</h2>
    <dl class="meta">
      <div><dt>Issued</dt><dd>{{ .Created }}</dd></div>
      <div><dt>Expires</dt><dd>{{ .Expires }}{{ if .ExpiresIn }} ({{ .ExpiresIn }} days){{ end }}</dd></div>
      <div><dt>Last used</dt><dd>{{ .LastUsed }}</dd></div>
    </dl>
	<p>Grafana does not return the secret again, and the delivery cookie has been
	cleared. The config below is the shape it goes in; paste your own copy into
	the token field.</p>

    {{ template "snippet" . }}

    <h2>Lost it?</h2>
    <form method="post" action="{{ .BasePath }}/token">
      <input type="hidden" name="csrf_token" value="{{ .CSRFToken }}">
      <button type="button" class="ghost" id="ask">Issue a new token</button>
      <div class="confirm" id="confirm" hidden>
        <p>Rotation creates a replacement, then revokes the old token. Update
        your MCP clients after copying the replacement. If cleanup fails,
        the page reports that older tokens may still work.</p>
        <div class="row">
          <button type="submit" class="danger">Yes, replace it</button>
          <button type="button" class="ghost" id="cancel">Cancel</button>
        </div>
      </div>
    </form>
    <noscript>
      <style nonce="{{ .CSPNonce }}">#ask { display: none; } #confirm[hidden] { display: block; }</style>
    </noscript>

    <h2>Remove access</h2>
    <form method="post" action="{{ .BasePath }}/revoke">
      <input type="hidden" name="csrf_token" value="{{ .CSRFToken }}">
      <button type="submit" class="ghost">Revoke all tokens</button>
    </form>
  </div>

  

{{ else }}
  <div class="card">
    <p>This issues a personal, read-only Grafana token for the MCP server. It is
	named from your verified email, so do not share it. It does not inherit your
	Grafana or OIDC role; the service always creates it with Viewer access.</p>

    <ul class="facts">
      <li>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M4 12.5l5 5L20 6.5" stroke="currentColor" stroke-width="2.2"
                stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>Read-only — the service account gets the Viewer role.</span>
      </li>
      <li>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M4 12.5l5 5L20 6.5" stroke="currentColor" stroke-width="2.2"
                stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>Expires in {{ .TTLDays }} days.</span>
      </li>
      <li>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M4 12.5l5 5L20 6.5" stroke="currentColor" stroke-width="2.2"
                stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>Shown once — copy it before you leave the page.</span>
      </li>
    </ul>

    <form method="post" action="{{ .BasePath }}/token">
      <input type="hidden" name="csrf_token" value="{{ .CSRFToken }}">
      <button type="submit" class="primary">Issue a token</button>
    </form>
  </div>
{{ end }}

  <footer>Signing out is the proxy's job, usually <code>{{ .BasePath }}/logout</code>.</footer>
</div>

{{ define "snippet" }}
<div class="clients"{{ with .Token }} data-token="{{ . }}"{{ end }}>
  <div class="modes" role="radiogroup" aria-label="Token storage">
    <button type="button" class="storage-mode mode on" data-storage="inline" role="radio" aria-checked="true">Token in configuration</button>
    <button type="button" class="storage-mode mode" data-storage="env" role="radio" aria-checked="false">1Password / env</button>
  </div>
  <div class="env-guide" hidden>
    <ol>
      <li>Save the token as <code>GRAFANA_SERVICE_ACCOUNT_TOKEN</code> in your 1Password Environment.</li>
      <li>Mount it as <code>.env</code> in your project. Keep your existing direnv setup,
      or add <code>dotenv .env</code> to <code>.envrc</code>, review it, and run <code>direnv allow</code>.</li>
      <li>Replace <code>/Users/example/work/project</code> below with your project directory.
      Install direnv and the selected launcher. Use absolute executable paths if your client cannot find them.</li>
    </ol>
    <div class="snippet">
      <div class="snippet-bar">
        <span class="filename">GRAFANA_SERVICE_ACCOUNT_TOKEN</span>
        <button type="button" class="copy"><span class="label">Copy token</span></button>
      </div>
      <pre><span class="tok">{{ .Mask }}</span></pre>
    </div>
    <p class="note">Saving to 1Password is manual. The env configuration contains no token.
    After rotation, update the Environment and restart the MCP server.</p>
  </div>
  <div class="tabs" role="tablist" aria-label="Client">
    {{ range $i, $f := .Formats }}
      <button type="button" class="tab{{ if eq $i 0 }} on{{ end }}" data-format="{{ $f.ID }}"
              role="tab" aria-selected="{{ if eq $i 0 }}true{{ else }}false{{ end }}">
        {{ $f.Icon }}<span>{{ $f.Name }}</span>
      </button>
    {{ end }}
  </div>

  {{ range $i, $f := .Formats }}
    <div class="panel" data-format="{{ $f.ID }}" role="tabpanel"{{ if ne $i 0 }} hidden{{ end }}>
      <div class="modes" role="radiogroup" aria-label="Launch mode">
        {{ range $j, $v := $f.Variants }}
          <button type="button" class="mode{{ if eq $j 0 }} on{{ end }}" data-mode="{{ $v.ID }}"
                  role="radio" aria-checked="{{ if eq $j 0 }}true{{ else }}false{{ end }}">{{ $v.Name }}</button>
        {{ end }}
      </div>
      {{ range $j, $v := $f.Variants }}
        <div class="variant" data-mode="{{ $v.ID }}"{{ if ne $j 0 }} hidden aria-hidden="true"{{ end }}>
          <div class="snippet">
            <div class="snippet-bar">
              <span class="filename">{{ $f.File }}</span>
              <button type="button" class="copy">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <rect x="9" y="9" width="11" height="11" rx="2.5" stroke="currentColor" stroke-width="1.8"/>
                  <path d="M5.5 15H5a1.5 1.5 0 01-1.5-1.5V5A1.5 1.5 0 015 3.5h8.5A1.5 1.5 0 0115 5v.5"
                        stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>
                </svg>
                <span class="label">Copy</span>
              </button>
            </div>
            <pre class="storage" data-storage="inline">{{ $v.Body }}</pre>
            <pre class="storage" data-storage="env" hidden>{{ $v.EnvBody }}</pre>
          </div>
          <p class="note">{{ $v.Note }}</p>
        </div>
      {{ end }}
      {{ with $f.Note }}<p class="note">{{ . }}</p>{{ end }}

    </div>
  {{ end }}

</div>

  <script type="module" src="{{ .ScriptURL }}"></script>
{{ end }}
`))

func (s *Server) render(w http.ResponseWriter, d pageData) {
	d.PublicURL = s.cfg.PublicURL
	d.BasePath = s.cfg.BasePath
	switch d.State {
	case stateIssued:
		d.Mask = strings.Repeat("*", maskWidth)
	case stateActive:
		d.Mask = "<your token>"
	}
	if d.Mask != "" {
		d.Formats = formats(d.PublicURL, d.Mask)
	}
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "could not prepare the page", http.StatusInternalServerError)
		return
	}
	d.CSPNonce = base64.RawURLEncoding.EncodeToString(nonce)
	d.StyleURL = s.assetURL("app.css")
	d.ScriptURL = s.assetURL("app.js")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The token must not survive in a shared cache or a proxy.
	w.Header().Set("Cache-Control", "no-store")
	// The stylesheet and the script are files now, so 'self' covers them; the
	// nonce remains only for the <noscript> rule, which cannot live in a file.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'nonce-"+d.CSPNonce+"'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	if err := page.Execute(w, d); err != nil {
		slog.Error("rendering the page", "err", err)
	}
}
