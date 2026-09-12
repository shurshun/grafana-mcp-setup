const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests',
  timeout: 90_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['line']],
  use: {
    browserName: 'chromium',
    headless: true,
    ignoreHTTPSErrors: true,
    trace: 'off',
    screenshot: 'off',
    video: 'off',
    launchOptions: {
      args: [
        '--host-resolver-rules=MAP app.integration 127.0.0.1,MAP keycloak.integration 127.0.0.1,MAP grafana.integration 127.0.0.1',
      ],
    },
  },
});
