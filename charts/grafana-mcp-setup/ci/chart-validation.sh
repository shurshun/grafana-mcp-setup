#!/usr/bin/env bash
set -euo pipefail

chart_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
render() {
  helm template "$1" "$chart_dir" -f "$chart_dir/ci/$2" "${@:3}"
}

native=$(render native native-values.yaml)
if render native native-values.yaml --set appPublicURL=http://grafana.example.com >/dev/null 2>&1; then
  echo 'native mode unexpectedly accepted a non-HTTPS public URL' >&2
  exit 1
fi
grep -Fq 'value: "native"' <<<"$native"
grep -Fq 'name: OIDC_CLIENT_SECRET' <<<"$native"
grep -Fq 'name: SESSION_COOKIE_KEY' <<<"$native"
grep -Fq 'kind: Ingress' <<<"$native"
if grep -Fq 'kind: SecurityPolicy' <<<"$native"; then
  echo 'native mode unexpectedly rendered an Envoy SecurityPolicy' >&2
  exit 1
fi

operator=$(render operator operator-values.yaml)
grep -Fq 'kind: GrafanaServiceAccount' <<<"$operator"
grep -Fq 'secretName: "grafana-mcp-setup-admin"' <<<"$operator"
grep -Fq 'key: token' <<<"$operator"
if grep -Fq 'name: operator-grafana-mcp-setup-grafana' <<<"$operator"; then
  echo 'operator mode unexpectedly rendered a Helm admin Secret' >&2
  exit 1
fi

proxy=$(render proxy ct-values.yaml)
grep -Fq 'value: "proxy"' <<<"$proxy"
if grep -Eq 'name: (OIDC_CLIENT_SECRET|SESSION_COOKIE_KEY)' <<<"$proxy"; then
  echo 'proxy mode unexpectedly rendered native credential environment variables' >&2
  exit 1
fi

compat=$(render compat ct-values.yaml \
  --set grafana.serviceAccountToken.name=leftover-name \
  --set grafana.serviceAccountToken.secretName=leftover-secret)
grep -Fq 'key: admin-token' <<<"$compat"
grep -Fq 'name: compat-grafana-mcp-setup-grafana' <<<"$compat"
if grep -Fq 'kind: GrafanaServiceAccount' <<<"$compat"; then
  echo 'create=false unexpectedly rendered a GrafanaServiceAccount' >&2
  exit 1
fi

native_existing=$(render native-existing native-values.yaml \
  --set oidc.existingSecret=oidc-client \
  --set auth.session.existingSecret=native-session \
  --set-string 'oidc.clientSecret=' \
  --set-string 'auth.session.key=')
grep -Fq 'name: oidc-client' <<<"$native_existing"
grep -Fq 'name: native-session' <<<"$native_existing"
if grep -Fq 'name: native-existing-grafana-mcp-setup-native-oidc' <<<"$native_existing" || \
   grep -Fq 'name: native-existing-grafana-mcp-setup-session' <<<"$native_existing"; then
  echo 'native existing Secret mode unexpectedly rendered duplicate Helm Secrets' >&2
  exit 1
fi

if render native native-values.yaml --set securityPolicy.enabled=true >/dev/null 2>&1; then
  echo 'native mode unexpectedly accepted securityPolicy.enabled' >&2
  exit 1
fi
if render operator operator-values.yaml --set grafana.adminToken=conflict >/dev/null 2>&1; then
  echo 'operator mode unexpectedly accepted grafana.adminToken' >&2
  exit 1
fi
if render operator operator-values.yaml --set-string 'grafana.serviceAccountToken.instanceName=' >/dev/null 2>&1; then
  echo 'operator mode unexpectedly accepted an empty instanceName' >&2
  exit 1
fi
if render operator operator-values.yaml --set grafana.existingSecret=conflict >/dev/null 2>&1; then
  echo 'operator mode unexpectedly accepted grafana.existingSecret' >&2
  exit 1
fi
if render native native-values.yaml --set-string oidc.clientSecret= >/dev/null 2>&1; then
  echo 'native mode unexpectedly accepted an empty OIDC client secret' >&2
  exit 1
fi
