# systemd deployment

Run one issuer process for each Grafana organization. The local mutex coordinates
that process only. Do not run a second host, a second unit, or a Kubernetes issuer
against the same accounts while using this mode.

The host needs systemd 247 or newer for `LoadCredential`, the release binary, and
an OIDC reverse proxy such as Envoy. systemd does not perform OIDC login. Keep the
issuer on loopback when the proxy runs on the same host. A remote proxy needs a
private listener protected by a firewall and TLS.

## Install

1. Install the verified release binary as `/usr/local/bin/grafana-mcp-setup`.
2. Copy `examples/systemd/config.env` to `/etc/grafana-mcp-setup/config.env`.
   Set the URLs, OIDC client, and allowed groups. The example uses the legacy
   Grafana API and needs no IAM flags. For `GRAFANA_API_MODE=iam`, first enable
   both Grafana feature flags and configure `GRAFANA_NAMESPACE`.
3. Provision the Grafana Admin token into
   `/etc/grafana-mcp-setup/grafana-admin-token` using your secret manager.
   Store only the token, without a variable assignment. The file must be owned
   by root and have mode `0600`.
4. Generate a persistent flash-cookie key. Keep the same key across restarts.
5. Install and start the unit.

```sh
sudo install -d -m 0700 /etc/grafana-mcp-setup
sudo install -m 0600 examples/systemd/config.env /etc/grafana-mcp-setup/config.env
sudo sh -c 'umask 077; openssl rand -base64 32 > /etc/grafana-mcp-setup/flash-cookie-key'
sudo install -m 0644 examples/systemd/grafana-mcp-setup.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now grafana-mcp-setup
curl --fail http://127.0.0.1:8080/readyz
```

Run the key-generation command only during initial provisioning. Overwriting a
key invalidates pending flash cookies. The command does not print the key.

`LoadCredential` supplies private runtime files to the dynamic service user.
Secrets do not belong in `config.env`, the unit, shell arguments, or journal
output. For encrypted host credentials, use `LoadCredentialEncrypted` and
`systemd-creds` where supported by the host.

## Proxy contract

The proxy must complete OIDC login, overwrite `X-Grafana-MCP-ID-Token` with the
verified user's ID token, and protect every route under `/setup-mcp`. The service
independently verifies signature, issuer, audience, expiry, and allowed groups.
Do not forward a client-supplied identity header unchanged.

Register `https://mcp.example.com/setup-mcp/oauth2/callback` with the provider
when using the supplied Envoy flow. Request `openid`, `profile`, `email`, and
`groups`. Use `SameSite=Lax` for the proxy's OIDC cookies. Public TLS terminates
at the proxy; `APP_PUBLIC_URL` must still use the public HTTPS URL.

## Operations

```sh
sudo systemctl status grafana-mcp-setup
sudo journalctl -u grafana-mcp-setup --since today
sudo systemctl restart grafana-mcp-setup
```

systemd stops the previous process before starting its replacement. SIGTERM
drains HTTP requests within the unit's stop timeout. `/healthz` checks the
process; `/readyz` checks Grafana capability with a cached result. Scrape
`http://127.0.0.1:8080/metrics` from a local monitoring agent.

After replacing either credential file, restart the unit. A flash cookie lasts
five minutes and is deleted after display, but a saved copy remains replayable
by the same authenticated identity until expiry. The service does not retain a
process-wide map of token secrets.

Stop the unit before running an administrative CLI command with `--apply` in
local-lock mode, then start it again after cleanup. A second CLI process has a
different mutex and cannot coordinate with the running HTTP service. Dry-run
commands do not mutate Grafana. Use the credential files through the `_FILE`
settings when running the CLI; the unit's private `%d` paths exist only inside
its own service execution context.

For example, stop the service, preview expired-token cleanup, then apply the
same command. The shell loads the supplied configuration file, so keep that
file root-owned and use shell-compatible assignments.

```sh
sudo systemctl stop grafana-mcp-setup
sudo sh -c '
  set -a
  . /etc/grafana-mcp-setup/config.env
  GRAFANA_ADMIN_TOKEN_FILE=/etc/grafana-mcp-setup/grafana-admin-token
  FLASH_COOKIE_KEY_FILE=/etc/grafana-mcp-setup/flash-cookie-key
  set +a
  exec /usr/local/bin/grafana-mcp-setup reconcile \
    --email user@example.com --delete-expired
'
```

After reviewing the output, repeat with `--apply`. Restart the service after
cleanup, including when abandoning the operation.

```sh
sudo systemctl start grafana-mcp-setup
```

Use `offboard --email departed@example.com` to preview account deletion, or
`reconcile --email user@example.com --keep-token EXACT_LIVE_TOKEN_NAME` to
preview removal of every other token. Both require `--apply` to change Grafana.
