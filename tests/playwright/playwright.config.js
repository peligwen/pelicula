// tests/playwright/playwright.config.js
const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './specs',
  timeout: 120_000,          // pipeline stages can take 60s+
  expect: { timeout: 10_000 },
  fullyParallel: false,      // stack is shared; run sequentially
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: './report', open: 'never' }]],
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:7399',
    trace: 'retain-on-failure',
    headless: true,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
