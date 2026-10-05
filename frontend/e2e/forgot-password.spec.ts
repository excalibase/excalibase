import { test, expect } from '@playwright/test';
import { mockVaultGuardReady } from './helpers';

// A signed-out user asks for a reset link from the sign-in page. The backend is
// stubbed; the Go handler's same-answer and limits are covered by its tests.
test.describe('forgot password', () => {
  test('sign-in links to the request page, which gives the same answer and the real expiry', async ({ page }) => {
    await mockVaultGuardReady(page);
    let sentBody: unknown = null;
    await page.route('**/api/email/reset/send', (route) => {
      sentBody = route.request().postDataJSON();
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ status: 'sent', expiresInMinutes: 60 }),
      });
    });

    await page.goto('/login');
    await page.getByRole('link', { name: 'Forgot password?' }).click();
    await expect(page).toHaveURL(/\/forgot-password$/);
    await expect(page).toHaveTitle('Forgot password · Excalibase Studio');

    await page.getByLabel('Email').fill('dev@example.com');
    await page.getByRole('button', { name: 'Send reset link' }).click();

    await expect(page.getByTestId('reset-link-sent')).toHaveText(
      "If an account exists for that address, we've sent a link. It expires in 60 minutes.",
    );
    expect(sentBody).toEqual({ email: 'dev@example.com' });

    await page.getByRole('link', { name: 'Back to sign in' }).click();
    await expect(page).toHaveURL(/\/login$/);
  });

  test('too many requests are reported', async ({ page }) => {
    await mockVaultGuardReady(page);
    await page.route('**/api/email/reset/send', (route) =>
      route.fulfill({ status: 429, contentType: 'application/json', body: JSON.stringify({ error: 'rate limit exceeded' }) }),
    );

    await page.goto('/forgot-password');
    await page.getByLabel('Email').fill('dev@example.com');
    await page.getByRole('button', { name: 'Send reset link' }).click();

    await expect(page.getByRole('alert')).toContainText('Too many reset requests');
  });
});
