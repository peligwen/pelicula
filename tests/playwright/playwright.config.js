// Playwright config for the Pelicula dashboard specs.
//
// Environment:
//   PELICULA_URL             base URL of a running stack (default http://localhost:7399,
//                            the port `make e2e` and the old `pelicula test` use)
//   PELICULA_ADMIN_USER      Jellyfin admin name (default "admin")
//   PELICULA_ADMIN_PASSWORD  Jellyfin admin password (required)
//   PELICULA_CHROMIUM_PATH   optional: use this Chromium binary instead of the one
//                            Playwright installed. Needed only when
//                            PLAYWRIGHT_BROWSERS_PATH is unset and the browser
//                            lives somewhere Playwright does not look.
const { defineConfig, devices } = require('@playwright/test');

const baseURL = process.env.PELICULA_URL || 'http://localhost:7399';

const launchOptions = {};
if (!process.env.PLAYWRIGHT_BROWSERS_PATH && process.env.PELICULA_CHROMIUM_PATH) {
  launchOptions.executablePath = process.env.PELICULA_CHROMIUM_PATH;
}

module.exports = defineConfig({
  testDir: './specs',
  timeout: 120_000,
  expect: { timeout: 10_000 },
  // The specs share one stack and one nginx login rate limit (10 r/min).
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { outputFolder: './report', open: 'never' }]],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    headless: true,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'], launchOptions },
    },
  ],
});
