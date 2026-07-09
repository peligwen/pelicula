// tests/playwright/playwright.config.js
const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './specs',
  timeout: 120_000,          // pipeline stages can take 60s+
  expect: { timeout: 10_000 },
  fullyParallel: false,      // stack is shared; run sequentially
  workers: 1,                // one worker total — avoids concurrent hits on the single Jellyfin/middleware auth path
  forbidOnly: !!process.env.CI,  // fail the build if a .only() slipped into a commit
  retries: process.env.CI ? 1 : 0,  // CI runners are noisier (shared vCPUs, cold caches); local dev stays deterministic
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
