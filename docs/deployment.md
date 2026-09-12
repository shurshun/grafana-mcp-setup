# Deployment and migration

## Compatibility

| Component | Required contract |
|---|---|
| Grafana 13.2.1, `legacy` (default) | Existing service-account API; no IAM flags |
| Grafana 13.2.1, `iam` | Enable `kubernetesServiceAccountsApi` and `kubernetesServiceAccountTokensApi` |
| Envoy Gateway 1.9 / Envoy 1.39 | Forward the ID token in `X-Grafana-MCP-ID-Token` |
| OIDC provider | Discovery, JWKS, authorization code flow, email and groups claims |
| Kubernetes | Namespaced coordination/v1 Lease for overlapping issuers |
| systemd | One issuer process; version 247 or newer for LoadCredential |

Grafana marks both IAM feature flags experimental. A plain Grafana 13.2.1
installation may return 404 for IAM discovery. The default `legacy` mode works
without those flags. The integration suite tests both modes, with flags disabled
for legacy and enabled for IAM. Older Grafana versions are not implied to be
supported by this test matrix.

The exact flags are declared in [Grafana 13.2.1's feature registry](https://github.com/grafana/grafana/blob/v13.2.1/pkg/services/featuremgmt/registry.go).
The [IAM OpenAPI snapshot](https://github.com/grafana/grafana/blob/v13.2.1/pkg/tests/apis/openapi_snapshots/iam.grafana.app-v0alpha1.json)
defines the resource and token request formats used by the client.

## Upgrade order

1. Choose `grafana.apiMode: legacy` to keep the existing Grafana API. For `iam`,
   first enable both IAM flags, preserving existing flags, and restart Grafana
   through the normal deployment process. Check IAM discovery and existing
   service-account reads before selecting `iam` in the issuer.
2. Provision a new persistent flash-cookie key and externally managed Admin and
   OIDC Secrets. Use new Secret names when moving away from chart-managed
   credentials. Helm may delete resources that disappear from its manifest.
3. Configure `appPublicURL`, `grafana.apiMode`, explicit allowed groups, the
   ID-token header, and Kubernetes locking. All cooperating issuer pods must
   use the same Kubernetes namespace, Lease, and flash-cookie key. Configure
   `grafana.namespace` when using the IAM API.
4. Drain the old issuer before the first upgrade. Version 0.2.1 does not
   participate in the Lease protocol, and its in-memory stash cannot be
   transferred into a flash cookie. Allow pending shows to finish or wait five
   minutes. A one-time maintenance window is required for this protocol change.
5. Deploy the matching application and chart release. Check `/readyz`, complete
   OIDC login, and verify issuance, display, rotation, and revocation using a
   designated test account.

Both APIs expose the same `mcp-<email>` accounts. The client converts numeric
legacy identifiers and IAM resource names into its common account/token model.
Changing `grafana.apiMode` selects the adapter on restart; it does not recreate
accounts or revoke tokens. Check `/readyz` and an explicit test identity after
switching. There is no automatic fallback if the selected API fails.

An existing 90-day token keeps its expiration. Do not use Grafana's global
`token_expiration_day_limit` to shorten it; that can also expire the issuer's
Admin token. Removing a group at the IdP blocks new issuance but does not revoke
previously issued static Grafana tokens.

## Locks

The binary defaults to `ROTATION_LOCK_MODE=local`. This is suitable for one
systemd process. The chart defaults to `rotationLock.mode=kubernetes`, with a
shared Lease and narrow RBAC. It uses a projected service-account token, while
`automountServiceAccountToken` remains false.

The Lease coordinates participating processes. It is not a Grafana transaction
or a fencing token enforced by Grafana. API timeouts and ownership checks before
each mutation limit overlapping work, but an external Admin or a severely paused process can
still race. Inspect and reconcile partial outcomes after infrastructure faults.
Never run an old issuer or a local-lock instance alongside Lease participants.
Even a one-replica, zero-surge Kubernetes Deployment can retain a terminating
old pod while its replacement starts. Local mode is intended for a single
systemd process or isolated development, not overlapping Kubernetes rollouts.

The issuer creates a missing Lease during readiness or lock acquisition.
Concurrent creators re-read the Lease after a conflict and preserve its owner.
Renewal never recreates a deleted Lease, so a holder loses its lock safely.
Issuers sharing Grafana accounts must use the same namespace and Lease name.
Separate namespaces with identically named Leases do not coordinate each other.

For Argo CD, set `rotationLock.createLease=false`. Argo CD installs the
application and RBAC; the issuer manages the runtime Lease. No CI bootstrap or
Argo CD resource-exclusion override is needed. This follows the
[ingress-nginx RBAC pattern](https://kubernetes.github.io/ingress-nginx/deploy/rbac/).
The Role grants namespaced Lease creation and restricts get/update/patch to the
configured Lease name. Kubernetes cannot restrict create by `resourceNames`.

The chart retains `rotationLock.createLease=true` by default so a Helm upgrade
does not prune an existing Helm-owned Lease. Preserve that object before changing
an existing Helm installation to runtime ownership. Do not set this flag to true
with Argo CD, which excludes Lease resources internally.

Avoid force-replacing or deleting an active Lease. The issuer preserves the
Lease's labels, annotations, finalizers, and owner references when renewing it.

## Secret updates

Supply Admin, OIDC, and flash-cookie credentials in values, or reference existing
Secrets. The chart adds a checksum for chart-managed Secrets. For externally
managed Secrets, configure your existing restart controller or perform a rolling
restart after the secret manager updates the resource. The app reads environment
credentials on startup.

Keep the flash-cookie key stable across ordinary rollouts. Key rotation
invalidates cookies encrypted with the previous key. Drain issuance and wait
five minutes before changing the key, then restart all issuer pods. Never print
the key or tokens in diagnostics.

## NetworkPolicy and metrics

`networkPolicy.enabled=true` requires explicit `ingressFrom` and `egress` rules.
Allow ingress from Envoy and the metrics scraper. Egress needs Grafana, IdP/JWKS,
DNS, and, in Kubernetes lock mode, the Kubernetes API server. Use your actual
pod selectors or destination CIDRs. Kubernetes NetworkPolicy has no portable
FQDN selector; do not pretend a hostname is an `ipBlock`.

```yaml
serviceMonitor:
  enabled: true
  labels:
    release: monitoring
  interval: 30s
```

Install the ServiceMonitor CRD before enabling this option. `/metrics` contains
bounded operational labels. Keep it private. Audit records must contain action,
result, and safe identifiers, never token values, cookies, Authorization headers,
or raw Grafana error bodies that might contain credentials.

HTTP audit events include issuer, a hash of the email identity, request ID,
result, and bounded failure reason codes. Issuance includes account and token
names. Revocation records the account ID and attempted/deleted counts instead
of an unbounded list of token names. Administrative CLI output records explicit
targets, account IDs, and planned or completed actions for operator review.

## Revocation and offboarding

Users can revoke their own token from the page. Offboarding additionally needs
an administrator to revoke the departed identity's account credentials, even
after that identity can no longer log in.

Review explicit target emails in dry-run mode before applying cleanup. Do not
infer offboarding merely from an account prefix or from a token's last-used
timestamp. Never delete the issuer's Admin account. Reconcile multiple live
tokens after a reported partial rotation failure and confirm that the retained
credential works before removing alternatives.

Run the CLI inside an issuer pod to reuse its configuration and shared Lease.
Each command defaults to a dry run. Add `--apply` only after reviewing its output.

```sh
kubectl -n observability exec deploy/grafana-mcp-setup -- \
  /grafana-mcp-setup reconcile --email user@example.com --delete-expired
kubectl -n observability exec deploy/grafana-mcp-setup -- \
  /grafana-mcp-setup reconcile --email user@example.com --delete-expired --apply
kubectl -n observability exec deploy/grafana-mcp-setup -- \
  /grafana-mcp-setup reconcile --email user@example.com --keep-token EXACT_LIVE_TOKEN_NAME
kubectl -n observability exec deploy/grafana-mcp-setup -- \
  /grafana-mcp-setup offboard --email departed@example.com
```

`--keep-token` requires one explicit email and an existing live token name. It
removes every other token only when `--apply` is supplied. Offboarding with
`--apply` deletes the exact Viewer service account and all its tokens. Remove
the identity's issuer access first so it cannot create the account again.
