// tests/playwright/playwright.config.js
const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './specs',
  timeout: 120_000,          // pipeline stages can take 60s+
  expect: { timeout: 10_000 },
  fullyParallel: false,      // stack is shared; run sequentially
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: 'tests/playwright/report', open: 'never' }]],
  use: {
    baseURL: 'http://localhost:7399',
    trace: 'on-first-retry',
    headless: true,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
