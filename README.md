# grafana-mcp-setup

Self-service Grafana tokens for MCP clients. Someone opens a page, signs in with
your existing identity provider, and gets a personal read-only token plus the
config block to paste into their client — Claude Code, Claude Desktop, Codex,
Cursor, VS Code or Zed, each in the shape and the file that client actually
reads. No administrator in the loop.

![The page after a token is issued](images/token.png)

## Why it exists

[`mcp-grafana`](https://github.com/grafana/mcp-grafana) authenticates with a
static service-account token. There is no OAuth and no on-behalf-of, so every
person who wants Grafana in their MCP client needs a token of their own.

Grafana OSS will not let them create one. Making a service account needs the
**Admin** role, RBAC with finer-grained roles is an Enterprise feature, and most
people are Viewers. The usual outcome is an administrator minting tokens by
hand, or — worse — one shared token passed around.

This service splits it: **identity from the user, privilege from the service.**
The person proves who they are through OIDC; the service holds the Admin token
and creates a Viewer service account named `mcp-<email>` for them.

## How it fits together

```
browser → https://grafana.example.com/setup-mcp
   → your proxy (Envoy Gateway SecurityPolicy, oauth2-proxy, …)
        └ OIDC login, ID token left in a cookie
   → grafana-mcp-setup:8080
        └ verifies the cookie, then uses its own Admin token to create
          the service account mcp-<email> with the Viewer role
```

Authentication belongs to the proxy, so this service carries no login code and
no session store. It verifies the ID token's signature against the issuer's JWKS
rather than trusting the cookie, because that cookie arrives over plain HTTP
inside the cluster.

The route is mounted under a prefix (`/setup-mcp` by default) on the Grafana
hostname. That keeps the config snippet same-origin and leaves Grafana's own
routes untouched — with the Gateway API, attach the policy to this `HTTPRoute`
rather than to the gateway.

## Authorisation

Signing in is authentication, not permission. The proxy in front authenticates
against the identity provider, not against Grafana, so Grafana's own rules —
`role_attribute_strict` and the rest — have no say over who reaches this page.
Without `oidc.requiredGroups`, anyone your provider lets in can issue themselves
a token, including people Grafana would refuse at its own login.

List the groups that may hold one and the provider decides. Any one of them is
enough, because an application's roles are usually separate groups: viewers in
one, admins in another, neither nested in the other.

The group has to reach the service in the `groups` claim, which means the proxy
must request the `groups` scope. Forget it and the claim is absent, every request
is refused, and it looks like a permissions problem rather than a missing scope.

## What a token is

- Service account `mcp-<email>`, role **Viewer**, whatever the person's own role
  in Grafana. MCP is read-only by policy, and the snippet adds
  `--disable-write` on top.
- Expires after `tokenTTLDays` (90 by default), set per token rather than
  through Grafana's global `token_expiration_day_limit` — that one would expire
  this service's own Admin token too.
- A reissue **rotates**: previous tokens are deleted before the new one is
  created, so nobody accumulates live credentials.
- Stored nowhere: not in a log, not in a database, not in a URL.

### Clients

The snippet comes in six flavours because the clients disagree about all three
of file, key and syntax: most read `mcpServers`, VS Code calls the same map
`servers` and wants a `type`, Zed wraps the command in an object of its own, and
Codex keeps TOML rather than JSON. Pick a tab and the block is ready to paste;
the choice is remembered for the next visit.

### Three states

`GET /setup-mcp` asks Grafana what the person already has.

| State | What the page shows |
|---|---|
| no live token | the pitch and an **Issue a token** button |
| a live token | its dates, the config shape with a placeholder, and a guarded reissue |
| just issued | the secret itself, masked, with a copy button |

![The page when a token already exists](images/status.png)

Grafana reveals a secret once and never again, so an existing token can be
described but not re-shown. The page says that plainly instead of implying a
second look is possible.

On screen the secret is a run of asterisks and the copy button holds the real
one in a data attribute. That defeats a shoulder and a screenshot, not the
reader — the page has to carry the token to the browser, so view-source still
shows it.

Reissue takes two clicks, with the consequence spelled out in between. Issuing
is post/redirect/get: the secret waits in memory under a single-use id and the
redirect spends it, so reloading the page that shows a token cannot repeat the
POST and rotate it behind the reader's back.

That stash is per-process, which is why the chart runs **one replica**. With
two, a redirect could land on the other pod and the page would say the token was
already shown.

![The landing page](images/landing.png)

## Install

```sh
helm install grafana-mcp-setup oci://ghcr.io/shurshun/charts/grafana-mcp-setup \
  --namespace monitoring \
  --set grafana.url=http://grafana.monitoring.svc \
  --set grafana.publicURL=https://grafana.example.com \
  --set grafana.adminToken=glsa_… \
  --set oidc.issuer=https://idp.example.com \
  --set oidc.requiredGroups={/grafana-mcp}
```

Two things have to exist first:

1. **A Grafana service account with the Admin role**, and its token in
   `grafana.adminToken` or an existing secret. Admin is not a preference —
   creating service accounts requires it.
2. **An OIDC client** whose redirect URI is
   `https://grafana.example.com/setup-mcp/oauth2/callback`, emitting the
   `groups` claim if you use `oidc.requiredGroups`.

Then route to it. With Envoy Gateway the chart can render both objects:

```yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: envoy-gateway
      namespace: envoy-gateway-system
      sectionName: http
  hostnames:
    - grafana.example.com

securityPolicy:
  enabled: true
  clientSecret: …
```

With ingress-nginx, enable `ingress` instead and put an authenticating proxy in
front — oauth2-proxy as a sidecar works, as long as it leaves the ID token in
the cookie named by `oidc.idTokenCookie`.

## Configuration

Everything comes from the environment; the chart sets it all.

| Variable | Default | Meaning |
|---|---|---|
| `GRAFANA_URL` | — | Grafana's in-cluster address |
| `GRAFANA_PUBLIC_URL` | — | What goes into the handed-out `.mcp.json` |
| `GRAFANA_ADMIN_TOKEN` | — | This service's own token |
| `OIDC_ISSUER` | — | Issuer whose JWKS verifies the ID token |
| `OIDC_CLIENT_ID` | `grafana-mcp-setup` | Expected `aud` |
| `REQUIRED_GROUPS` | empty | Comma-separated groups; holding any one allows a token, empty allows anyone who can sign in |
| `ID_TOKEN_COOKIE` | `mcp_id_token` | Cookie the proxy leaves the ID token in |
| `BASE_PATH` | `/setup-mcp` | Where the service is mounted |
| `TOKEN_TTL_DAYS` | `90` | Lifetime of an issued token |
| `LISTEN_ADDR` | `:8080` | Listen address |

## Two things this does not solve

**Offboarding.** A Grafana service account is not tied to a user, so deleting
the person's directory account does not revoke their token. Removing
`mcp-<email>` is a separate step and belongs in your offboarding checklist, or
the token outlives the person for its full lifetime.

**The Admin token.** Compromising this pod means compromising Grafana admin.
The service does exactly one thing with it, but the blast radius is what it is,
and Grafana OSS offers no narrower role for creating service accounts.

## Development

```sh
go test ./...
docker build -f Dockerfile.local -t grafana-mcp-setup:dev .
helm template test charts/grafana-mcp-setup --set grafana.adminToken=x
```

## Licence

MIT.
