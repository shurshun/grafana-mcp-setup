#!/usr/bin/env bash
set -euo pipefail

namespace=integration
release=grafana-mcp-setup
grafana_api_mode=${GRAFANA_API_MODE:-legacy}
grafana_port_forward_pid=
app_port_forward_pid=
tls_dir=

cleanup() {
  if [[ -n "$grafana_port_forward_pid" ]]; then
    kill "$grafana_port_forward_pid" 2>/dev/null || true
  fi
  if [[ -n "$app_port_forward_pid" ]]; then
    kill "$app_port_forward_pid" 2>/dev/null || true
  fi
  if [[ -n "$tls_dir" ]]; then
    rm -f "$tls_dir/tls.crt" "$tls_dir/tls.key"
    rmdir "$tls_dir" 2>/dev/null || true
  fi
}
trap cleanup EXIT

if [[ "$grafana_api_mode" != legacy && "$grafana_api_mode" != iam ]]; then
  echo 'GRAFANA_API_MODE must be legacy or iam' >&2
  exit 1
fi

wait_for_nested_condition() {
  local resource=$1
  local name=$2
  local expression=$3

  for _ in {1..60}; do
    if kubectl -n "$namespace" get "$resource" "$name" -o json | jq --exit-status "$expression" >/dev/null; then
      return 0
    fi
    sleep 2
  done
  return 1
}

kubectl apply -f integration/gateway.yaml
tls_dir=$(mktemp -d /tmp/grafana-mcp-integration-tls.XXXXXX)
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$tls_dir/tls.key" \
  -out "$tls_dir/tls.crt" \
  -days 1 \
  -subj /CN=app.integration \
  -addext subjectAltName=DNS:app.integration >/dev/null 2>&1
kubectl -n "$namespace" create secret tls integration-tls \
  --cert "$tls_dir/tls.crt" \
  --key "$tls_dir/tls.key"
kubectl apply -f integration/keycloak.yaml
kubectl apply -f integration/grafana.yaml
if [[ "$grafana_api_mode" == iam ]]; then
  kubectl -n "$namespace" set env deployment/grafana \
    GF_FEATURE_TOGGLES_ENABLE=kubernetesServiceAccountsApi,kubernetesServiceAccountTokensApi
fi

kubectl -n "$namespace" rollout status deployment/keycloak --timeout=180s
kubectl -n "$namespace" rollout status deployment/grafana --timeout=180s

kubectl -n "$namespace" port-forward service/grafana 3000:3000 >/tmp/grafana-integration-port-forward.log 2>&1 &
grafana_port_forward_pid=$!

for _ in {1..60}; do
  if curl --fail --silent --show-error http://127.0.0.1:3000/api/health >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error http://127.0.0.1:3000/api/health >/dev/null

if [[ "$grafana_api_mode" == iam ]]; then
  service_accounts_url=http://127.0.0.1:3000/apis/iam.grafana.app/v0alpha1/namespaces/default/serviceaccounts
  for _ in {1..60}; do
    if curl --fail --silent \
      --user integration-admin:integration-admin-password \
      "$service_accounts_url?limit=1" >/dev/null; then
      break
    fi
    sleep 1
  done
  curl --fail --silent --show-error \
    --user integration-admin:integration-admin-password \
    "$service_accounts_url?limit=1" >/dev/null

  curl --fail --silent --show-error \
    --user integration-admin:integration-admin-password \
    --header 'Content-Type: application/json' \
    --data '{"apiVersion":"iam.grafana.app/v0alpha1","kind":"ServiceAccount","metadata":{"name":"grafana-mcp-setup-integration"},"spec":{"title":"grafana-mcp-setup-integration","role":"Admin","disabled":false}}' \
    "$service_accounts_url" >/dev/null
  admin_token_json=$(curl --fail --silent --show-error \
    --user integration-admin:integration-admin-password \
    --header 'Content-Type: application/json' \
    --data '{"tokenName":"integration-admin","expiresInSeconds":3600}' \
    "$service_accounts_url/grafana-mcp-setup-integration/tokens")
  admin_token=$(jq --exit-status --raw-output '.token' <<<"$admin_token_json")
else
  service_accounts_url=http://127.0.0.1:3000/api/serviceaccounts
  for _ in {1..60}; do
    if curl --fail --silent \
      --user integration-admin:integration-admin-password \
      "$service_accounts_url/search?query=integration-readiness" >/dev/null; then
      break
    fi
    sleep 1
  done
  curl --fail --silent --show-error \
    --user integration-admin:integration-admin-password \
    "$service_accounts_url/search?query=integration-readiness" >/dev/null

  iam_status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
    --user integration-admin:integration-admin-password \
    http://127.0.0.1:3000/apis/iam.grafana.app/v0alpha1)
  test "$iam_status" = 404

  admin_account_json=$(curl --fail --silent --show-error \
    --user integration-admin:integration-admin-password \
    --header 'Content-Type: application/json' \
    --data '{"name":"grafana-mcp-setup-integration","role":"Admin","isDisabled":false}' \
    "$service_accounts_url")
  admin_account_id=$(jq --exit-status --raw-output '.id' <<<"$admin_account_json")
  admin_token_json=$(curl --fail --silent --show-error \
    --user integration-admin:integration-admin-password \
    --header 'Content-Type: application/json' \
    --data '{"name":"integration-admin","secondsToLive":3600}' \
    "$service_accounts_url/$admin_account_id/tokens")
  admin_token=$(jq --exit-status --raw-output '.key' <<<"$admin_token_json")
fi

printf '%s' "$admin_token" | kubectl -n "$namespace" create secret generic grafana-admin-token \
  --from-file=admin-token=/dev/stdin
kubectl -n "$namespace" create secret generic flash-cookie \
  --from-literal=flash-cookie-key=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
kubectl -n "$namespace" create secret generic oidc-client \
  --from-literal=client-secret=integration-client-secret

helm upgrade --install "$release" charts/grafana-mcp-setup \
  --namespace "$namespace" \
  --values integration/values.yaml \
  --set "grafana.apiMode=$grafana_api_mode" \
  --wait \
  --timeout 3m

kubectl -n "$namespace" get lease grafana-mcp-integration -o json |
  jq -e '.metadata.annotations["meta.helm.sh/release-name"] == null' >/dev/null

kubectl -n "$namespace" wait --for=condition=Accepted gateway/integration --timeout=180s
route_accepted='any(.status.parents[]?.conditions[]?; .type == "Accepted" and .status == "True")'
policy_accepted='any(.status.ancestors[]?.conditions[]?; .type == "Accepted" and .status == "True")'
wait_for_nested_condition httproute keycloak "$route_accepted"
wait_for_nested_condition httproute grafana "$route_accepted"
wait_for_nested_condition httproute grafana-mcp-setup "$route_accepted"
wait_for_nested_condition securitypolicy grafana-mcp-setup "$policy_accepted"

for _ in {1..60}; do
  proxy_service=$(kubectl -n envoy-gateway-system get services \
    -l gateway.envoyproxy.io/owning-gateway-name=integration \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  proxy_ports=$(kubectl -n envoy-gateway-system get service "$proxy_service" \
    -o jsonpath='{.spec.ports[*].port}' 2>/dev/null || true)
  [[ "$proxy_ports" == *"80"* && "$proxy_ports" == *"443"* ]] && break
  sleep 1
done
test -n "$proxy_service"

proxy_patch=$(kubectl -n envoy-gateway-system get service "$proxy_service" -o json | jq '{
  spec: {
    type: "NodePort",
    ports: [.spec.ports[] |
      if .port == 80 then .nodePort = 30080
      elif .port == 443 then .nodePort = 30443
      else . end |
      {name, nodePort, port, protocol, targetPort}
    ]
  }
}')
kubectl -n envoy-gateway-system patch service "$proxy_service" \
  --type=merge \
  --patch "$proxy_patch"

envoy_image=$(kubectl -n envoy-gateway-system get pods \
  -l gateway.envoyproxy.io/owning-gateway-name=integration \
  -o jsonpath='{.items[0].spec.containers[0].image}')
test "$envoy_image" = docker.io/envoyproxy/envoy:distroless-v1.39.0

kubectl -n "$namespace" port-forward service/grafana-mcp-setup 18080:8080 >/tmp/grafana-mcp-integration-port-forward.log 2>&1 &
app_port_forward_pid=$!

for _ in {1..60}; do
  if curl --fail --silent --show-error \
    --resolve keycloak.integration:80:127.0.0.1 \
    http://keycloak.integration/realms/integration/.well-known/openid-configuration >/dev/null && \
     curl --fail --silent --show-error http://127.0.0.1:18080/readyz >/dev/null; then
    curl --fail --insecure --silent --show-error \
      --resolve app.integration:443:127.0.0.1 \
      https://app.integration/setup-mcp >/dev/null && exit 0
  fi
  sleep 1
done

kubectl get gateway,httproute -n "$namespace" -o wide
kubectl get securitypolicy -n "$namespace" -o yaml
kubectl get pods -A -o wide
exit 1
