# Repository guidance

`AGENTS.md` links to this file. Keep the link intact.

## Architecture

The service issues personal Grafana Viewer service-account tokens after OIDC
login. The proxy forwards `X-Grafana-MCP-ID-Token`; the service verifies its
signature, issuer, audience, expiry, email, and allowed groups. Raw-cookie mode
requires explicit configuration. Email remains the account identity.

Viewer service accounts do not inherit the person's Grafana permissions. Keep
that warning in the UI and README. Every generated client mode uses stdio and
`--disable-write`.

- `config.go` validates environment settings and credential files. Empty groups
  fail startup unless `ALLOW_ALL_AUTHENTICATED_USERS=true` explicitly permits all.
- The Grafana client defaults to `GRAFANA_API_MODE=legacy`; `iam` is explicit.
  Only IAM needs the two documented Grafana feature flags. Both adapters share
  rotation and security logic. Readiness checks the selected API. Never switch
  APIs automatically after an error or during a mutation.
- `server.go` handles issue, show, revoke, authentication, CSRF, and audit events.
- `lock.go` coordinates mutations with a local mutex or Kubernetes Lease.
- `stash.go` encrypts flash cookies and signs CSRF tokens. It stores no token map.
- `reconcile.go` provides explicit-target administrative cleanup with dry-run
  defaults. `--apply` changes Grafana.
- `page.go`, `formats.go`, and `highlight.go` render the client configurations.
- `metrics.go` exports bounded operational metrics without identity labels.

Rotation creates a new token before deleting old tokens. Ambiguous deletion
failures preserve the replacement and report partial cleanup. Never roll back a
replacement after an old-token deletion may have succeeded.

POST returns a 303 redirect to `{BASE_PATH}/token`. The encrypted flash cookie
carries the secret across replicas that share its key. It expires after five
minutes and is cleared after display. A saved cookie remains replayable by the
same identity until expiry; do not describe this as strict single-use storage.

The binary defaults to one-process local locking; the chart defaults to a shared
Kubernetes Lease. Lease ownership cannot fence an in-flight Grafana request.
For deployment changes, read [deployment notes](docs/deployment.md), including
initial migration and secret rotation. For host deployment, read
[systemd instructions](docs/systemd.md).

## Verification

```sh
go build ./...
go vet ./...
go mod tidy -diff
go test -race ./...
golangci-lint run
helm lint charts/grafana-mcp-setup -f charts/grafana-mcp-setup/ci/routed-values.yaml
helm template test charts/grafana-mcp-setup -f charts/grafana-mcp-setup/ci/routed-values.yaml
docker build -f Dockerfile.local -t grafana-mcp-setup:dev .
scripts/screenshots.sh   # retake the README images
```

Use the Go toolchain and linter versions pinned in the workflow. The release
Dockerfile consumes GoReleaser's prebuilt binary. To verify proxy behavior or
Lease concurrency, run the real TLS, Keycloak, Envoy, and Grafana suite described
in [integration/README.md](integration/README.md). A static discovery fixture
cannot verify authentication or token lifecycle.

The client, launcher and storage switches share one tray (`.toolbar`). Every
client offers the same three launchers, so that row is rendered once at the top
level rather than per panel — the script already drove every `.variant` by
`data-mode`, whichever panel it sat in.

Marks come from simple-icons (CC0 files, trademarks still their owners'); Codex
and VS Code keep drawn glyphs because simple-icons ships neither. Each control
sets `--brand`, and the block's rail follows the chosen client through
`data-format` on `.clients`. Keep those palette selectors scoped to the controls
(`.tab[data-format=…]`, `.mode[data-mode=…]`, `.storage-mode[data-storage=…]`):
panels and the `<pre>` blocks carry the same attributes, and an unscoped rule
lets a variant overwrite the brand for everything inside it. Dark overrides the
brands that are too dark to read on it — Cursor's is black, Zed's a deep blue.

The stylesheet and the page script are files under `internal/server/assets`,
embedded in the binary and served at `{BASE_PATH}/assets/app.<hash>.css|js`. The
hash comes from the content, so the response is immutable-cacheable and a
release reaches every reader without a stale copy. That is what lets the policy
say `script-src 'self'` instead of carrying a nonce, and what keeps the page
itself from re-sending 12KB of CSS and JS on every render. The script is a
module, so its names never reach the window — a page-level `const` collides with
whatever a browser extension declares, and the resulting parse error kills the
whole block. The one inline rule left is the `<noscript>` block, which is why
`style-src` still carries a nonce.

`scripts/social-preview.sh` renders `images/social-preview.png` from
`scripts/social-preview.html` at 1280x640 and 2x, filling every icon placeholder
from `icons.go` so the card cannot advertise a mark the page no longer uses.
GitHub does not read that file from the repository; upload it under Settings ->
Social preview.

`scripts/screenshots.sh` renders the pages with `TestDumpPages`, so the images
carry the documented example data rather than a live stack's hostnames. It pins
the dark palette by dropping the `prefers-color-scheme` query, reads each page's
height back through its title because Chrome has no full-page screenshot flag,
and shoots at 1000px and 2x. Do not crop afterwards: `sips -c` crops from the
centre and takes the heading off the top. The integration suite captures the
same pages for CI artifacts; those are test output, not documentation.

Keep tokens, cookies, raw upstream error bodies, and token-bearing HTML out of
logs and CI artifacts. Test credentials belong only to disposable fixtures.
Tests should cover failure outcomes, not only successful responses.

## Delivery

The release depends on Go checks, chart validation, and the real integration
suite. Security workflows scan the image and filesystem, run govulncheck and
CodeQL, and audit GitHub Actions with zizmor.

Follow the [release contract](code.md). Before tagging a release, update both
`version` and `appVersion` in `Chart.yaml` to the application release version.
Publish the application and chart together. Image and chart paths use
`GITHUB_REPOSITORY_OWNER`, including in forks. GoReleaser needs `syft` for SBOMs.

Every commit requires a `Signed-off-by` trailer matching its author. Use
`git commit -s` when a commit is authorized. Workflow changes require the GitHub
`workflow` token scope when merging; an interactive auth refresh may be needed.

Keep `runAsUser: 65532` for the distroless non-root image. Keep OIDC cookies
`SameSite=Lax` so cross-origin provider callbacks work. Keep chart-test
credentials excluded from production secret and misconfiguration scans.

Write concise comments describing current behavior and its reason. Preserve
printable snippet sentinels and HTML escaping so copied config text remains
unchanged by syntax highlighting.
