# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A self-service page that issues personal, read-only Grafana service-account tokens for MCP clients. A reverse proxy in front of `BASE_PATH` (default `/setup-mcp`) runs the OIDC dance and leaves an ID token in a cookie; this service verifies that token against the issuer's JWKS and then uses its own **Admin** token to create `mcp-<email>` with the **Viewer** role. Identity from the user, privilege from the service — because creating a service account needs Admin and Grafana OSS has no role between that and Viewer.

The page renders the config in six client shapes (Claude Code, Claude Desktop, Codex, Cursor, VS Code, Zed), masks the secret on screen, and copies the real one from a data attribute.

## Commands

```bash
go build ./...
go vet ./...
golangci-lint run                     # config in .golangci.yml (v2 schema)
go test ./...                         # add -race; CI always runs with it
DUMP_DIR=/tmp/shots go test -run TestDumpPages ./internal/server   # write each page state to HTML
docker build -f Dockerfile.local -t grafana-mcp-setup:dev .        # Dockerfile alone needs goreleaser's binary
goreleaser release --snapshot --clean
helm lint charts/grafana-mcp-setup
helm template t charts/grafana-mcp-setup --set grafana.adminToken=x
kind create cluster --name ct && kubectl apply -f charts/grafana-mcp-setup/ci/fake-idp.yaml && ct install --charts charts/grafana-mcp-setup
```

## Delivery

CI is `goreleaser.yml` (lint + test, release gated on `refs/tags/v*`), `helm.yml` (chart lint, kubeconform, `ct install` into kind), `security.yml` (trivy → code scanning). A `v*` tag publishes both `ghcr.io/OWNER/grafana-mcp-setup:<version>` and `oci://ghcr.io/OWNER/charts/grafana-mcp-setup`. **The chart's published version comes from the tag** (`helm package --version ${TAG#v}`), not from `Chart.yaml` — that file's `version` is only what a local `helm package` would use.

Both are templated off `GITHUB_REPOSITORY_OWNER`, so a fork's own tag publishes under the fork.

**Every commit needs a `Signed-off-by` trailer (`git commit -s`)** — a DCO probot check fails the PR without it, and the trailer's name/email must match the author.

**Merging a PR that touches `.github/workflows/` needs the `workflow` scope** on the token: `gh pr merge` otherwise fails with "refusing to allow an OAuth App to create or update workflow … without `workflow` scope". Fix with `gh auth refresh -h github.com -s workflow`, which is an interactive device-code flow.

`goreleaser` shells out to `syft` for the archive SBOMs; the release job installs it with `anchore/sbom-action/download-syft`. Without that step the release dies at "software bill of materials" with `executable file not found`.

## Architecture

`cmd/grafana-mcp-setup/main.go` reads `server.FromEnv()`, calls `server.New` (which discovers the OIDC provider — a failure here is fatal, on purpose: the service cannot do its one job without it) and serves `Server.Handler()`.

`internal/server` is the whole of it:

- `config.go` — every knob, all from the environment.
- `server.go` — routes, `identity`, `authorise`, `issue`, `liveToken`.
- `grafana.go` — the Grafana API client, admin-token-authenticated.
- `stash.go` — one-time secret store between POST and the redirect that shows it.
- `page.go` — the HTML, its CSS and its script, as one template.
- `formats.go` — one snippet per client.
- `highlight.go` — the syntax painter.

### Three states, and a redirect

`GET {BASE_PATH}` asks Grafana what the caller already holds and renders one of `stateNone`, `stateActive` (a token exists; its dates, not its secret) or `stateIssued` (the one moment the secret exists).

`POST {BASE_PATH}/token` **does not render the secret.** It stashes it under a random single-use id and answers `303` to `{BASE_PATH}/token/<id>`. Rendering it from the POST meant a refresh repeated the POST and silently rotated the token the reader had just copied. Don't collapse this back into one handler.

**That stash is per-process, which is why the chart pins `replicas: 1`.** With two pods the redirect can land on the other one and the page says "already shown".

### Authorisation

`REQUIRED_GROUPS` is the whole of it, and holding any one of them is enough (application roles tend to be separate groups, so requiring a single one locks out the admins). Empty means anyone the provider admits.

**The proxy authenticates against the identity provider, not against Grafana.** Grafana's own `role_attribute_strict` has no say over who reaches this page, so an empty list hands a Grafana token to anyone who can sign in — including people Grafana would refuse at its own login. This was a real mistake in the first deployment.

For the group to arrive at all, the proxy must request the `groups` scope. Omit it and the claim is absent, every request is refused, and it reads as a permissions problem rather than a missing scope.

### The masked token

`formats.go` builds each snippet with `tokenSentinel` where the secret goes; `highlight.go` paints the text and turns that sentinel into `<span class="tok">`, carrying the mask. The real token sits **once** on `.clients[data-token]`, and the copy handler swaps the mask for it in `pre.textContent`.

The sentinel has to be printable: `%q` escapes a control character, and the scanner then no longer recognises it.

`highlight.go` escapes every piece with `template.HTMLEscape` before writing its own tags, which is why the two `template.HTML(...)` conversions carry `#nosec G203`. `TestHighlightingKeepsTheText` strips the tags back off and compares against the source — what the reader copies is that text, so painting must not change it.

## Gotchas that cost time

**`runAsNonRoot` with a distroless base.** The image's `USER` is the name `nonroot`, which kubelet cannot check, so the container never starts: `container has runAsNonRoot and image has non-numeric user`. The chart therefore sets `runAsUser: 65532` explicitly. This fails in any cluster, not only in kind.

**The kind install test needs an issuer.** The service resolves OIDC at startup, so against a fake hostname the pod crash-loops and `ct install` waits for a readiness that never comes. `charts/grafana-mcp-setup/ci/fake-idp.yaml` serves a static discovery document for exactly this, and `ci/*-values.yaml` point at it.

**Trivy reads `ci/` as if it were a deliverable** — eighteen hardening findings against an nginx pod that serves one file — so `security.yml` skips that directory.

**The OIDC cookie must be `SameSite=Lax`.** The provider redirects back from its own origin and `Strict` withholds the cookie on exactly that hop, which surfaces as `CSRF_token_validation_failed` rather than anything cookie-shaped.

**Screenshots: `sips -c` crops from the centre.** Trimming trailing background also takes the heading off the top. Shoot at the page's own height with `--window-size` and don't crop afterwards.

**A Deployment previously applied by Helm and then adopted by ArgoCD can end up with both containers.** Server-side apply merges the two managers' lists rather than replacing, so the old container stays and crash-loops against secrets the new chart no longer creates. Delete the Deployment and let the controller recreate it; patching the container away leaves the old manager's `managedFields` in place.

## Conventions

Prose in comments and commit messages: plain sentences that say why, not what. No verbless fragments, no colon-hinged labels. Comments describe present behaviour rather than what the code used to do.

Tests are named for the claim they make (`TestSpentLinkSaysSoWithoutRotating`), and the cases that must fail matter more than the ones that pass.
