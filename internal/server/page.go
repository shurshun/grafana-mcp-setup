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
<style nonce="{{ .CSPNonce }}">
  :root {
    color-scheme: light dark;
    --bg: #f6f7f9;
    --card: #ffffff;
    --ink: #14171c;
    --muted: #5b6472;
    --line: #e3e6ea;
    --accent: #f46800;
    --code-bg: #11141a;
    --code-ink: #e6e9ef;
    --code-line: #262c36;
    --warn-bg: #fff7ed;
    --warn-line: #f59e0b;
    --warn-ink: #7c4a03;
    --radius: 12px;
  }

  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0e1116;
      --card: #161a21;
      --ink: #e6e9ef;
      --muted: #99a2b2;
      --line: #262c36;
      --code-bg: #0b0e13;
      --code-line: #222833;
      --warn-bg: #2a1e0c;
      --warn-line: #b45309;
      --warn-ink: #fcd9a4;
    }
  }

  * { box-sizing: border-box; }

  body {
    margin: 0;
    padding: 2.5rem 1.25rem 4rem;
    background: var(--bg);
    color: var(--ink);
    font: 15px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
    -webkit-font-smoothing: antialiased;
  }

  .shell { max-width: 44rem; margin: 0 auto; }

  header { display: flex; align-items: center; gap: .85rem; margin-bottom: 1.75rem; }
  header svg { flex: none; }
  h1 { font-size: 1.35rem; line-height: 1.25; margin: 0; letter-spacing: -0.01em; }
  .sub { margin: .15rem 0 0; color: var(--muted); font-size: .9rem; }

  .chip {
    margin-left: auto;
    display: inline-flex; align-items: center; gap: .45rem;
    padding: .35rem .75rem;
    border: 1px solid var(--line); border-radius: 999px;
    background: var(--card); color: var(--muted);
    font-size: .82rem; white-space: nowrap;
  }
  .chip::before {
    content: ""; width: .5rem; height: .5rem; border-radius: 50%;
    background: #22c55e;
  }

  .card {
    background: var(--card);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 1.5rem;
  }

  h2 {
    font-size: .78rem; text-transform: uppercase; letter-spacing: .08em;
    color: var(--muted); margin: 1.75rem 0 .6rem; font-weight: 600;
  }
  h2:first-of-type { margin-top: 0; }

  p { margin: 0 0 .9rem; }
  p:last-child { margin-bottom: 0; }

  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .88em;
    background: color-mix(in srgb, var(--ink) 7%, transparent);
    padding: .1rem .35rem; border-radius: 4px;
  }

  .callout {
    display: flex; gap: .7rem;
    background: var(--warn-bg); color: var(--warn-ink);
    border: 1px solid color-mix(in srgb, var(--warn-line) 45%, transparent);
    border-left: 3px solid var(--warn-line);
    border-radius: 8px; padding: .8rem .95rem;
    font-size: .9rem; margin-bottom: 1.5rem;
  }
  .callout svg { flex: none; margin-top: .15rem; }
  .callout p { margin: 0; }

  /* Tabs -------------------------------------------------------------- */

  /* One control rather than six buttons: a tray with the segments inside it,
     and the active one raised out of the tray. */
  .tabs {
    display: inline-flex; flex-wrap: wrap; gap: 2px;
    margin-bottom: .9rem; padding: 3px;
    border: 1px solid var(--line); border-radius: 10px;
    background: color-mix(in srgb, var(--ink) 5%, var(--card));
  }
  .tab {
    display: inline-flex; align-items: center; gap: .4rem;
    padding: .4rem .7rem; font-size: .85rem;
    border: 1px solid transparent; border-radius: 7px;
    background: transparent; color: var(--muted);
  }
  .tab svg { color: var(--client, var(--muted)); opacity: .75; }
  .tab:hover { color: var(--ink); }
  .tab.on {
    color: var(--ink); background: var(--card);
    border-color: var(--line);
    box-shadow: 0 1px 2px rgba(0, 0, 0, .25);
  }
  .tab.on svg { opacity: 1; }

  /* One colour per client, so the tab and the block it opens agree. */
  [data-format="claude-code"]    { --client: #d97757; }
  [data-format="claude-desktop"] { --client: #d97757; }
  [data-format="codex"]          { --client: #10a37f; }
  [data-format="cursor"]         { --client: #7c8797; }
  [data-format="vscode"]         { --client: #3b82f6; }
  [data-format="zed"]            { --client: #8b5cf6; }

  .note { margin: .7rem 0 0; color: var(--muted); font-size: .85rem; }

  .modes { display: flex; flex-wrap: wrap; gap: .4rem; margin: 0 0 .7rem; }
  .mode { padding: .3rem .65rem; font-size: .8rem; }
  .mode.on { border-color: var(--accent); color: var(--ink); }

  .snippet {
    border: 1px solid var(--code-line); border-radius: 10px; overflow: hidden;
    border-top: 2px solid color-mix(in srgb, var(--client, var(--accent)) 70%, transparent);
  }
  .snippet-bar {
    display: flex; align-items: center; gap: .75rem;
    background: color-mix(in srgb, var(--code-bg) 92%, #fff);
    border-bottom: 1px solid var(--code-line);
    padding: .45rem .5rem .45rem .9rem;
  }
  .filename {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .8rem; color: #9aa3b2; margin-right: auto;
  }

  pre {
    margin: 0; padding: 1rem;
    background: var(--code-bg); color: var(--code-ink);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .85rem; line-height: 1.55;
    overflow-x: auto;
  }
  /* Syntax ------------------------------------------------------------ */

  pre .k   { color: #7dd3fc; }  /* keys */
  pre .s   { color: #b6e08a; }  /* strings */
  pre .n   { color: #f3b27a; }  /* numbers */
  pre .t   { color: #d6a2f0; }  /* TOML tables */
  pre .tok { color: var(--accent); letter-spacing: .04em; }

  button {
    font: inherit; cursor: pointer; border-radius: 8px;
    border: 1px solid var(--line); background: var(--card); color: var(--ink);
    padding: .6rem 1.1rem;
    transition: background .12s ease, border-color .12s ease, transform .06s ease;
  }
  button:hover { border-color: color-mix(in srgb, var(--ink) 28%, transparent); }
  button:active { transform: translateY(1px); }
  button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

  .copy {
    display: inline-flex; align-items: center; gap: .4rem;
    padding: .35rem .7rem; font-size: .82rem;
    background: #1d232d; color: #d7dce5; border-color: #333b47;
  }
  .copy:hover { background: #262e3a; border-color: #46505f; }
  .copy.ok { background: #14532d; border-color: #1c6b3a; color: #d9f5e3; }

  .primary {
    background: var(--accent); border-color: var(--accent); color: #fff;
    font-weight: 600; padding: .7rem 1.4rem;
  }
  .primary:hover { background: #d95c00; border-color: #d95c00; }

  .ghost { font-size: .92rem; padding: .55rem 1rem; }
  .danger {
    background: #b91c1c; border-color: #b91c1c; color: #fff; font-weight: 600;
  }
  .danger:hover { background: #a01818; border-color: #a01818; }

  .confirm {
    margin-top: .9rem; padding: .9rem 1rem;
    border: 1px solid color-mix(in srgb, #b91c1c 35%, transparent);
    border-radius: 8px;
    background: color-mix(in srgb, #b91c1c 8%, transparent);
  }
  .confirm p { margin: 0 0 .8rem; font-size: .9rem; }
  .confirm .row { display: flex; gap: .6rem; flex-wrap: wrap; }

  .meta { display: flex; flex-wrap: wrap; gap: 1.75rem; margin: 0 0 1.4rem; }
  .meta div { min-width: 6rem; }
  .meta dt {
    font-size: .72rem; text-transform: uppercase; letter-spacing: .07em;
    color: var(--muted); margin-bottom: .15rem;
  }
  .meta dd { margin: 0; font-size: .95rem; font-variant-numeric: tabular-nums; }

  .facts { list-style: none; padding: 0; margin: 0 0 1.5rem; }
  .facts li {
    display: flex; gap: .6rem; align-items: flex-start;
    padding: .5rem 0; border-top: 1px solid var(--line);
    font-size: .92rem;
  }
  .facts li:last-child { border-bottom: 1px solid var(--line); }
  .facts svg { flex: none; margin-top: .28rem; color: var(--accent); }

  footer { margin-top: 1.25rem; color: var(--muted); font-size: .85rem; }

  /* On a phone the heading and the chip cannot share a line. */
  @media (max-width: 30rem) {
    header { flex-wrap: wrap; }
    .chip { flex-basis: 100%; margin-left: 0; }
    .card { padding: 1.15rem; }
  }
</style>

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

  <script nonce="{{ .CSPNonce }}">
    // Scoped: every name here would otherwise be a global, and a page that
    // declares one an extension already holds dies with a SyntaxError before a
    // single line runs.
    (() => {
    const ask = document.getElementById("ask");
    const confirmBox = document.getElementById("confirm");
    ask.addEventListener("click", () => { confirmBox.hidden = false; ask.hidden = true; });
    document.getElementById("cancel").addEventListener("click", () => {
      confirmBox.hidden = true;
      ask.hidden = false;
    });
    })();
  </script>

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

  <script nonce="{{ .CSPNonce }}">
    // Scoped for the same reason as above: the name clients in particular
    // collides with what browser extensions put on the window.
    (() => {
    const clients = document.querySelector(".clients");
    let token = clients.dataset.token;
    let tokenCopied = false;
    const tabs = [...document.querySelectorAll(".tab")];
    const panels = [...document.querySelectorAll(".panel")];
	const modeButtons = [...document.querySelectorAll(".mode[data-mode]")];
	const variants = [...document.querySelectorAll(".variant")];

    function show(id) {
      const known = tabs.some((t) => t.dataset.format === id);
      if (!known) return;
      tabs.forEach((t) => {
        const on = t.dataset.format === id;
        t.classList.toggle("on", on);
        t.setAttribute("aria-selected", on);
      });
      panels.forEach((p) => { p.hidden = p.dataset.format !== id; });
      try { localStorage.setItem("mcp-client", id); } catch {}
    }

    tabs.forEach((t) => t.addEventListener("click", () => show(t.dataset.format)));

	function showMode(id) {
	  if (!modeButtons.some((button) => button.dataset.mode === id)) return;
	  modeButtons.forEach((button) => {
	    const on = button.dataset.mode === id;
	    button.classList.toggle("on", on);
	    button.setAttribute("aria-checked", on);
	  });
	  variants.forEach((variant) => {
	    variant.hidden = variant.dataset.mode !== id;
	    variant.setAttribute("aria-hidden", variant.hidden);
	  });
	  try { localStorage.setItem("mcp-launch-mode", id); } catch {}
	}

	modeButtons.forEach((button) => button.addEventListener("click", () => showMode(button.dataset.mode)));

    // Remembering the choice is a convenience; a browser that refuses storage
    // just starts on the first tab.
    try {
      const saved = localStorage.getItem("mcp-client");
      if (saved) show(saved);
	  const savedMode = localStorage.getItem("mcp-launch-mode");
	  if (savedMode) showMode(savedMode);
    } catch {}

    let storage = "inline";
    document.querySelectorAll(".storage-mode").forEach((button) => {
      button.addEventListener("click", () => {
        storage = button.dataset.storage;
        document.querySelectorAll(".storage-mode").forEach((item) => {
          const on = item.dataset.storage === storage;
          item.classList.toggle("on", on);
          item.setAttribute("aria-checked", on);
        });
        document.querySelectorAll(".storage").forEach((pre) => { pre.hidden = pre.dataset.storage !== storage; });
        document.querySelector(".env-guide").hidden = storage !== "env";
        document.querySelectorAll(".copy").forEach((copy) => {
          const pre = copy.closest(".snippet").querySelector("pre:not([hidden])");
          copy.disabled = tokenCopied && !!pre.querySelector(".tok");
        });
      });
    });

    document.querySelectorAll(".copy").forEach((copy) => {

      const block = copy.closest(".snippet");
      const label = copy.querySelector(".label");
      const originalLabel = label.textContent;

      copy.addEventListener("click", async () => {
        const snippet = block.querySelector("pre:not([hidden])");
        const masked = snippet.querySelector(".tok");
        // A function replacement keeps $-sequences in the token literal.
        const real = token && masked
          ? snippet.textContent.replace(masked.textContent, () => token)
          : snippet.textContent;
        try {
          await navigator.clipboard.writeText(real);
          label.textContent = "Copied";
          copy.classList.add("ok");
          if (token && masked) {
            tokenCopied = true;
            token = "";
            delete clients.dataset.token;
            document.querySelectorAll(".tok").forEach((node) => { node.textContent = "[token copied]"; });
            document.querySelectorAll(".copy").forEach((button) => {
              if (button.closest(".snippet").querySelector("pre:not([hidden]) .tok")) button.disabled = true;
            });
          }
        } catch {
          // Clipboard access can be refused. Unmask first, or a hand-made
          // selection copies the asterisks.
          if (token && masked) masked.textContent = token;
          getSelection().selectAllChildren(snippet);
          label.textContent = "Press Ctrl/Cmd+C";
        }
        setTimeout(() => { label.textContent = originalLabel; copy.classList.remove("ok"); }, 2000);
      });
    });
    })();
  </script>
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The token must not survive in a shared cache or a proxy.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+d.CSPNonce+"'; script-src 'nonce-"+d.CSPNonce+"'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	if err := page.Execute(w, d); err != nil {
		slog.Error("rendering the page", "err", err)
	}
}
