const http = require('node:http');
const https = require('node:https');
const fs = require('node:fs/promises');
const path = require('node:path');
const { execFileSync, spawn } = require('node:child_process');
const { expect, test } = require('@playwright/test');

const APP_URL = 'https://app.integration/setup-mcp';
const GRAFANA_API_MODE = process.env.GRAFANA_API_MODE || 'legacy';

async function readmeScreenshot(page, name) {
  const directory = process.env.README_SCREENSHOT_DIR;
  if (!directory) return;
  if ((await page.locator('body').innerText()).includes('glsa_')) {
    throw new Error('refusing to capture a visible token');
  }
  await fs.mkdir(directory, { recursive: true });
  await page.screenshot({ path: path.join(directory, `${name}.png`), fullPage: true });
}

if (!['legacy', 'iam'].includes(GRAFANA_API_MODE)) {
  throw new Error('GRAFANA_API_MODE must be legacy or iam');
}

function requestThroughGateway(host, path, options = {}) {
  return requestLocal(options.tls ? 443 : 80, path, {
    ...options,
    headers: { host, ...(options.headers || {}) },
  });
}

function requestLocal(port, path, options = {}) {
  return new Promise((resolve, reject) => {
    const client = options.tls ? https : http;
    const request = client.request({
      host: '127.0.0.1',
      port,
      method: options.method || 'GET',
      path,
      rejectUnauthorized: false,
      headers: options.headers || {},
    }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => resolve({
        status: response.statusCode,
        headers: response.headers,
        body: Buffer.concat(chunks).toString('utf8'),
      }));
    });
    request.on('error', reject);
    if (options.body) request.write(options.body);
    request.end();
  });
}

async function login(page, username, password) {
  await page.goto(APP_URL);
  await expect(page).toHaveURL(/keycloak\.integration/);
  await page.locator('#username').fill(username);
  await page.locator('#password').fill(password);
  await page.locator('#kc-login').click();
}

function basic(value) {
  return `Basic ${Buffer.from(value).toString('base64')}`;
}

async function grafana(path, authorization) {
  return requestThroughGateway('grafana.integration', path, {
    headers: { authorization },
  });
}

async function findServiceAccount(title) {
  if (GRAFANA_API_MODE === 'iam') {
    const admin = basic('integration-admin:integration-admin-password');
    const response = await grafana(
      '/apis/iam.grafana.app/v0alpha1/namespaces/default/serviceaccounts',
      admin,
    );
    expect(response.status).toBe(200);
    const item = JSON.parse(response.body).items.find((candidate) => candidate.spec.title === title);
    if (!item) return null;
    return {
      id: item.metadata.name,
      role: item.spec.role,
      disabled: item.spec.disabled,
    };
  }

  return findLegacyServiceAccount(title);
}

async function findLegacyServiceAccount(title) {
  const admin = basic('integration-admin:integration-admin-password');
  const response = await grafana(
    `/api/serviceaccounts/search?query=${encodeURIComponent(title)}&perpage=100&page=1`,
    admin,
  );
  expect(response.status).toBe(200);
  const item = JSON.parse(response.body).serviceAccounts.find((candidate) => candidate.name === title);
  if (!item) return null;
  return {
    id: String(item.id),
    role: item.role,
    disabled: item.isDisabled,
  };
}

async function createLegacyServiceAccount(title) {
  const body = JSON.stringify({ name: title, role: 'Viewer', isDisabled: false });
  const created = await requestThroughGateway('grafana.integration', '/api/serviceaccounts', {
    method: 'POST',
    headers: {
      authorization: basic('integration-admin:integration-admin-password'),
      'content-type': 'application/json',
      'content-length': Buffer.byteLength(body),
    },
    body,
  });
  expect(created.status).toBe(201);
  const account = JSON.parse(created.body);
  return { id: String(account.id), role: account.role, disabled: account.isDisabled };
}

async function listLegacyServiceAccountTokens(account) {
  const admin = basic('integration-admin:integration-admin-password');
  const response = await grafana(
    `/api/serviceaccounts/${encodeURIComponent(account.id)}/tokens`,
    admin,
  );
  expect(response.status).toBe(200);
  return JSON.parse(response.body);
}

async function listServiceAccountTokens(account) {
  const admin = basic('integration-admin:integration-admin-password');
  const path = GRAFANA_API_MODE === 'iam'
    ? `/apis/iam.grafana.app/v0alpha1/namespaces/default/serviceaccounts/${encodeURIComponent(account.id)}/tokens`
    : `/api/serviceaccounts/${encodeURIComponent(account.id)}/tokens`;
  const response = await grafana(path, admin);
  expect(response.status).toBe(200);
  const parsed = JSON.parse(response.body);
  return Array.isArray(parsed) ? parsed : parsed.items;
}

async function issuedToken(page) {
  return page.locator('.clients').getAttribute('data-token');
}

function assertGrafanaToken(token) {
  if (typeof token !== 'string' || !token.startsWith('glsa_')) {
    throw new Error('Grafana did not return a service account token');
  }
}

function attribute(body, expression, description) {
  const match = body.match(expression);
  if (!match) throw new Error(`response did not contain ${description}`);
  return match[1];
}

async function startPortForward(pod, port) {
  const child = spawn(
    'kubectl',
    ['-n', 'integration', 'port-forward', `pod/${pod}`, `${port}:8080`],
    { stdio: 'ignore' },
  );
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (child.exitCode !== null) throw new Error('kubectl port-forward exited early');
    try {
      const response = await requestLocal(port, '/healthz');
      if (response.status === 200) return child;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  child.kill();
  throw new Error('kubectl port-forward did not become ready');
}

function mutationHeaders(idToken, body, cookie) {
  return {
    host: 'app.integration',
    'x-grafana-mcp-id-token': idToken,
    origin: 'https://app.integration',
    'sec-fetch-site': 'same-origin',
    'content-type': 'application/x-www-form-urlencoded',
    'content-length': Buffer.byteLength(body),
    ...(cookie ? { cookie } : {}),
  };
}

test('Envoy authenticates with OIDC and forwards a verified ID token', async ({ browser }) => {
  const unauthenticated = await requestThroughGateway('app.integration', '/setup-mcp', {
    tls: true,
    headers: { 'x-grafana-mcp-id-token': 'forged' },
  });
  expect(unauthenticated.status).toBe(302);
  expect(unauthenticated.headers.location).toContain('keycloak.integration');

  const context = await browser.newContext({
    viewport: { width: 1000, height: 800 },
    colorScheme: 'dark',
    permissions: ['clipboard-read', 'clipboard-write'],
    extraHTTPHeaders: { 'X-Grafana-MCP-ID-Token': 'forged' },
  });
  await context.addInitScript(() => {
    let attempts = 0;
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async () => {
          attempts += 1;
          if (attempts === 1) throw new Error('test clipboard refusal');
        },
      },
    });
  });
  const page = await context.newPage();
  await login(page, 'allowed', 'allowed-password');
  await expect(page).toHaveURL(APP_URL);
  await expect(page.getByText('allowed@example.com')).toBeVisible();
  await readmeScreenshot(page, 'landing');

  const cookies = await context.cookies('https://app.integration');
  const cookieHeader = cookies.map(({ name, value }) => `${name}=${value}`).join('; ');
  const forwarded = await requestThroughGateway('app.integration', '/setup-mcp', {
    tls: true,
    headers: {
      cookie: cookieHeader,
      'x-grafana-mcp-id-token': 'forged',
    },
  });
  expect(forwarded.status).toBe(200);

  const badOriginBody = new URLSearchParams({ csrf_token: 'invalid' }).toString();
  const badOrigin = await requestThroughGateway('app.integration', '/setup-mcp/token', {
    tls: true,
    method: 'POST',
    headers: {
      cookie: cookieHeader,
      origin: 'http://attacker.integration',
      'content-type': 'application/x-www-form-urlencoded',
      'content-length': Buffer.byteLength(badOriginBody),
    },
    body: badOriginBody,
  });
  expect(badOrigin.status).toBe(403);

  const missingCSRF = await requestThroughGateway('app.integration', '/setup-mcp/token', {
    tls: true,
    method: 'POST',
    headers: {
      cookie: cookieHeader,
      origin: 'https://app.integration',
      'sec-fetch-site': 'same-origin',
      'content-type': 'application/x-www-form-urlencoded',
      'content-length': 0,
    },
  });
  expect(missingCSRF.status).toBe(403);

  const migratedLegacyAccount = GRAFANA_API_MODE === 'iam'
    ? await createLegacyServiceAccount('mcp-allowed@example.com')
    : null;

  await page.getByRole('button', { name: 'Issue a token' }).click();
  await expect(page).toHaveURL(`${APP_URL}/token`);
  const firstToken = await issuedToken(page);
  assertGrafanaToken(firstToken);
  await page.getByRole('tab', { name: 'Codex', exact: true }).click();
  await readmeScreenshot(page, 'token');
  await page.getByRole('radio', { name: '1Password / env', exact: true }).click();
  await readmeScreenshot(page, 'environment');
  await page.getByRole('radio', { name: 'Token in configuration', exact: true }).click();

  const firstWorks = await grafana('/api/org', `Bearer ${firstToken}`);
  expect(firstWorks.status).toBe(200);

  const copy = page.locator('.panel:not([hidden]) .variant:not([hidden]) .copy').first();
  await copy.click();
  const revealed = await page.locator('.panel:not([hidden]) .variant:not([hidden]) .tok').first().textContent();
  if (revealed !== firstToken) throw new Error('clipboard fallback did not reveal the token');
  await copy.click();
  const tokenRemainsInDOM = await page.locator('.tok').evaluateAll(
    (nodes, secret) => nodes.some((node) => node.textContent.includes(secret)),
    firstToken,
  );
  if (tokenRemainsInDOM) throw new Error('successful copy left the token in the DOM');
  expect(await page.locator('.copy').evaluateAll((buttons) => buttons.every((button) => button.disabled))).toBe(true);

  await page.getByRole('radio', { name: '1Password / env', exact: true }).click();
  for (const client of ['Claude Code', 'Claude Desktop', 'Codex', 'Cursor', 'VS Code', 'Zed']) {
    await page.getByRole('tab', { name: client, exact: true }).click();
    for (const mode of ['Installed binary', 'uvx', 'Docker']) {
      await page.getByRole('radio', { name: mode, exact: true }).click();
      const config = page.locator('.panel:not([hidden]) .variant:not([hidden]) pre:not([hidden])');
      const text = await config.textContent();
      expect(text).toContain('/Users/example/work/project');
      expect(text).toContain('--disable-write');
      if (text.includes(firstToken) || text.includes('glsa_')) throw new Error('env config contains token');
      await expect(page.locator('.panel:not([hidden]) .variant:not([hidden]) .copy')).toBeEnabled();
    }
  }
  await page.locator('.panel:not([hidden]) .variant:not([hidden]) .copy').click();
  await expect(page.locator('.env-guide .copy')).toBeDisabled();

  await page.reload();
  expect(await issuedToken(page)).toBeNull();
  await page.getByRole('radio', { name: '1Password / env', exact: true }).click();
  await page.getByRole('radio', { name: 'Token in configuration', exact: true }).click();
  await expect(page.locator('.panel:not([hidden]) .variant:not([hidden]) .copy')).toBeEnabled();
  await page.getByRole('tab', { name: 'Codex', exact: true }).click();
  await page.getByRole('radio', { name: 'Installed binary', exact: true }).click();
  await readmeScreenshot(page, 'status');

  await page.getByRole('button', { name: 'Issue a new token' }).click();
  await page.getByRole('button', { name: 'Yes, replace it' }).click();
  const secondToken = await issuedToken(page);
  assertGrafanaToken(secondToken);
  if (secondToken === firstToken) throw new Error('rotation returned the previous token');

  const oldToken = await grafana('/api/org', `Bearer ${firstToken}`);
  const newToken = await grafana('/api/org', `Bearer ${secondToken}`);
  expect(oldToken.status).toBe(401);
  expect(newToken.status).toBe(200);

  const account = await findServiceAccount('mcp-allowed@example.com');
  expect(account).toBeTruthy();
  expect(account.role).toBe('Viewer');
  expect(account.disabled).toBe(false);
  expect(await listServiceAccountTokens(account)).toHaveLength(1);
  if (GRAFANA_API_MODE === 'iam') {
    const legacyAccount = await findLegacyServiceAccount('mcp-allowed@example.com');
    if (!legacyAccount) throw new Error('legacy API lost the migration fixture service account');
    expect(legacyAccount.id).toBe(migratedLegacyAccount.id);
    expect(legacyAccount.role).toBe('Viewer');
    expect(legacyAccount.disabled).toBe(false);
    expect(await listLegacyServiceAccountTokens(legacyAccount)).toHaveLength(1);
  }

  await page.reload();
  expect(await issuedToken(page)).toBeNull();
  await page.getByRole('button', { name: /revoke/i }).click();
  await expect(page.getByRole('button', { name: 'Issue a token' })).toBeVisible();
  const revoked = await grafana('/api/org', `Bearer ${secondToken}`);
  expect(revoked.status).toBe(401);
  expect(await listServiceAccountTokens(account)).toHaveLength(0);

  await context.close();
});

test('two replicas serialize rotation and share encrypted flash state', async () => {
  const tokenBody = new URLSearchParams({
    grant_type: 'password',
    client_id: 'grafana-mcp-setup',
    client_secret: 'integration-client-secret',
    username: 'allowed',
    password: 'allowed-password',
    scope: 'openid groups',
  }).toString();
  const oidc = await requestThroughGateway(
    'keycloak.integration',
    '/realms/integration/protocol/openid-connect/token',
    {
      method: 'POST',
      headers: {
        'content-type': 'application/x-www-form-urlencoded',
        'content-length': Buffer.byteLength(tokenBody),
      },
      body: tokenBody,
    },
  );
  if (oidc.status !== 200) throw new Error('test OIDC provider did not issue an ID token');
  const idToken = JSON.parse(oidc.body).id_token;
  if (typeof idToken !== 'string') throw new Error('test OIDC response omitted the ID token');

  const pods = execFileSync(
    'kubectl',
    [
      '-n', 'integration', 'get', 'pods',
      '-l', 'app.kubernetes.io/name=grafana-mcp-setup',
      '-o', 'jsonpath={range .items[*]}{.metadata.name}{"\\n"}{end}',
    ],
    { encoding: 'utf8' },
  ).trim().split('\n').filter(Boolean);
  expect(pods).toHaveLength(2);

  const forwards = [];
  try {
    forwards.push(await startPortForward(pods[0], 18081));
    forwards.push(await startPortForward(pods[1], 18082));

    const [firstPage, secondPage] = await Promise.all([
      requestLocal(18081, '/setup-mcp', {
        headers: { host: 'app.integration', 'x-grafana-mcp-id-token': idToken },
      }),
      requestLocal(18082, '/setup-mcp', {
        headers: { host: 'app.integration', 'x-grafana-mcp-id-token': idToken },
      }),
    ]);
    expect(firstPage.status).toBe(200);
    expect(secondPage.status).toBe(200);
    const firstCSRF = attribute(firstPage.body, /name="csrf_token" value="([^"]+)"/, 'CSRF token');
    const secondCSRF = attribute(secondPage.body, /name="csrf_token" value="([^"]+)"/, 'CSRF token');

    const issue = (port, csrf) => {
      const body = new URLSearchParams({ csrf_token: csrf }).toString();
      return requestLocal(port, '/setup-mcp/token', {
        method: 'POST',
        headers: mutationHeaders(idToken, body),
        body,
      }).then((response) => ({ response, port }));
    };
    const issued = await Promise.all([issue(18081, firstCSRF), issue(18082, secondCSRF)]);
    for (const result of issued) expect(result.response.status).toBe(303);

    const delivered = await Promise.all(issued.map(async (result) => {
      const setCookie = result.response.headers['set-cookie'];
      const flashCookie = (Array.isArray(setCookie) ? setCookie : [setCookie])
        .find((value) => value && value.startsWith('grafana_mcp_flash='));
      if (!flashCookie) throw new Error('rotation response omitted the encrypted flash cookie');
      const showPort = result.port === 18081 ? 18082 : 18081;
      const shown = await requestLocal(showPort, '/setup-mcp/token', {
        headers: {
          host: 'app.integration',
          'x-grafana-mcp-id-token': idToken,
          cookie: flashCookie.split(';', 1)[0],
        },
      });
      expect(shown.status).toBe(200);
      const token = attribute(shown.body, /data-token="([^"]+)"/, 'one-time token');
      assertGrafanaToken(token);
      const check = await grafana('/api/org', `Bearer ${token}`);
      return { token, showPort, status: check.status };
    }));
    expect(delivered.map(({ status }) => status).sort()).toEqual([200, 401]);
    const activeDelivery = delivered.find(({ status }) => status === 200);
    if (!activeDelivery) throw new Error('neither concurrent rotation returned the active token');
    const { token, showPort } = activeDelivery;

    const account = await findServiceAccount('mcp-allowed@example.com');
    if (!account) throw new Error('rotation did not retain the expected service account');
    expect(await listServiceAccountTokens(account)).toHaveLength(1);

    const active = await requestLocal(showPort, '/setup-mcp', {
      headers: { host: 'app.integration', 'x-grafana-mcp-id-token': idToken },
    });
    expect(active.status).toBe(200);
    const csrf = attribute(active.body, /name="csrf_token" value="([^"]+)"/, 'CSRF token');
    const revokeBody = new URLSearchParams({ csrf_token: csrf }).toString();
    const revoked = await requestLocal(showPort, '/setup-mcp/revoke', {
      method: 'POST',
      headers: mutationHeaders(idToken, revokeBody),
      body: revokeBody,
    });
    expect(revoked.status).toBe(303);
    expect((await grafana('/api/org', `Bearer ${token}`)).status).toBe(401);
    expect(await listServiceAccountTokens(account)).toHaveLength(0);
  } finally {
    for (const child of forwards) child.kill();
  }
});

test('application denies a valid identity outside the required group', async ({ browser }) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(APP_URL);
  await page.locator('#username').fill('denied');
  await page.locator('#password').fill('denied-password');
  const responsePromise = page.waitForResponse(
    (response) => response.url() === APP_URL && response.status() === 403,
  );
  await page.locator('#kc-login').click();
  const response = await responsePromise;
  expect(response.status()).toBe(403);
  await context.close();
});
