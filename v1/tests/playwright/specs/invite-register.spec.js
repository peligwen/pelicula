// Admin creates an invite in Settings; a new browser context opens the link,
// registers, and lands on the dashboard as a viewer (no Settings tab).
const { test, expect } = require('@playwright/test');
const {
  BASE_URL,
  adminAPI,
  browserContextFor,
  deleteUser,
  uniqueUsername,
  uniquePassword,
  valueOrText,
} = require('../helpers');

test.describe('invite and register', () => {
  let admin;
  let username;

  test.beforeAll(async () => {
    admin = await adminAPI();
  });

  test.afterAll(async () => {
    if (admin) {
      if (username) await deleteUser(admin, username);
      await admin.dispose();
    }
  });

  test('a new user registers from an invite link as a viewer', async ({ browser }) => {
    // Admin side: create the invite through the Settings tab.
    const adminCtx = await browserContextFor(browser, admin);
    const adminPage = await adminCtx.newPage();
    await adminPage.goto('/');
    await adminPage.getByTestId('tab-settings').click();
    await adminPage.getByTestId('invite-create').click();

    const link = adminPage.getByTestId('invite-link').first();
    await expect(link).toBeVisible();
    const inviteURL = new URL(await valueOrText(link), BASE_URL).toString();
    expect(inviteURL).toContain('/register');
    expect(inviteURL).toContain('code=');
    await adminCtx.close();

    // Invitee side: a context with no session.
    username = uniqueUsername('pw-reg');
    const password = uniquePassword();
    const inviteeCtx = await browser.newContext({ baseURL: BASE_URL });
    const page = await inviteeCtx.newPage();
    await page.goto(inviteURL);
    await page.getByTestId('register-username').fill(username);
    await page.getByTestId('register-password').fill(password);
    await page.getByTestId('register-confirm').fill(password);
    await page.getByTestId('register-submit').click();

    // Registration signs the user in and redirects to the dashboard.
    await page.waitForURL((u) => new URL(u).pathname === '/');
    await expect(page.getByTestId('tab-search')).toBeVisible();
    await expect(page.getByTestId('tab-requests')).toBeVisible();
    await expect(page.getByTestId('tab-settings')).toBeHidden(); // viewers have no Settings
    await inviteeCtx.close();
  });
});
