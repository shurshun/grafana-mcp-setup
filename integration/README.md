# Integration test

This test runs production-shaped authentication and token management in a
disposable kind cluster.

| Component | Version | Purpose |
|---|---:|---|
| Grafana | 13.2.1 | Legacy and `iam.grafana.app/v0alpha1` service-account APIs |
| Envoy Gateway | 1.9.0 | OIDC `SecurityPolicy` and ID-token forwarding |
| Envoy | 1.39.0 | OAuth flow and owned-header replacement |
| Keycloak | 26.0.4 | Test-only OpenID Connect provider |
| Application | current source | Issue, show, rotate, and revoke flow |

Keycloak has one allowed user and one user outside `/grafana-mcp`. Credentials,
client secret, Grafana password, and flash key only exist in this disposable
test setup. Its password grant is enabled only to drive deterministic requests
to both application pods during the Lease and flash-cookie test. Do not reuse
these credentials or settings.

GitHub Actions runs the suite through `.github/workflows/integration.yml`. For a
local run, ports 80 and 443 must be free and Docker must be available.

```bash
docker build -f Dockerfile.local -t grafana-mcp-setup:integration .
kind create cluster --name integration --config integration/kind.yaml
kind load docker-image grafana-mcp-setup:integration --name integration
helm install envoy-gateway oci://docker.io/envoyproxy/gateway-helm \
  --version v1.9.0 --namespace envoy-gateway-system --create-namespace \
  --wait --timeout 3m
GRAFANA_API_MODE=legacy integration/bootstrap.sh
cd integration
npm ci --ignore-scripts
npx playwright install chromium
GRAFANA_API_MODE=legacy npm test
```

Repeat the clean cluster run with `GRAFANA_API_MODE=iam` to enable and test both
experimental IAM feature flags. The legacy run leaves both flags disabled. The
GitHub workflow runs both modes as a matrix with the same browser and two-pod
lifecycle suite.

Set `AUTH_MODE=native` for both `integration/bootstrap.sh` and `npm test` to
exercise application-owned OIDC. The native fixture uses Envoy only for TLS
routing, without a SecurityPolicy. It also installs Grafana Operator v5.25.0
in the disposable cluster, registers the test Grafana instance, and enables
`grafana.serviceAccountToken.create`. The application must become ready using
the operator-generated Secret before browser tests start.

CI runs all four combinations of `proxy`/`native` and `legacy`/`iam`. Both auth
modes test real browser login, denied group membership, issuance, rotation,
revocation, and cross-replica flash cookies. The native replica test obtains
its session through authorization-code login and proves that an otherwise valid
forwarded ID token alone cannot authenticate directly to a pod.

Use a fresh disposable cluster for each combination. Set a dedicated
`KUBECONFIG` when running locally so fixtures cannot target a company cluster.

The browser maps `app.integration`, `keycloak.integration`, and
`grafana.integration` to the kind NodePort. Cluster workloads resolve the same
names through Kubernetes service DNS. This keeps issuer and callback URLs
identical on both sides of the browser redirect.

The workflow does not retain browser traces, screenshots, videos, Kubernetes
logs, or HTML because they could contain a one-time Grafana token or OIDC
session cookie.
