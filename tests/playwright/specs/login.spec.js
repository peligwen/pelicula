// Login overlay: a wrong password shows an inline error, the right one shows the tabs.
const { test, expect } = require('@playwright/test');
const { ADMIN_USER, adminPassword } = require('../helpers');

test.describe('login', () => {
  test('a bad password shows an inline error', async ({ page }) => {
    await page.goto('/');
    await page.getByTestId('login-username').fill(ADMIN_USER);
    await page.getByTestId('login-password').fill('this-is-not-the-password');
    await page.getByTestId('login-submit').click();

    const err = page.getByTestId('login-error');
    await expect(err).toBeVisible();
    await expect(err).not.toHaveText('');
    // Still on the login form.
    await expect(page.getByTestId('login-username')).toBeVisible();
  });

  test('the admin password signs in and shows the tabs', async ({ page }) => {
    await page.goto('/');
    await page.getByTestId('login-username').fill(ADMIN_USER);
    await page.getByTestId('login-password').fill(adminPassword());
    await page.getByTestId('login-submit').click();

    await expect(page.getByTestId('login-username')).toBeHidden();
    await expect(page.getByTestId('tab-search')).toBeVisible();
    await expect(page.getByTestId('tab-requests')).toBeVisible();
    await expect(page.getByTestId('tab-jobs')).toBeVisible();
    await expect(page.getByTestId('tab-settings')).toBeVisible(); // admin only
  });
});
