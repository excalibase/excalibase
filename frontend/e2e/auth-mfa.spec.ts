import { test, expect, type Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints, mockProjectRole } from './helpers';

const SETTINGS = { siteUrl: 'https://app.example.com', requireEmailVerification: false };
const END_USERS = { users: [{ id: 1, email: 'alice@test.com', role: 'user', allowedRoles: ['user'] }], total: 1 };

async function mockFeatures(page: Page, features: Record<string, boolean>) {
  await page.route('**/api/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'selfhosted', features }) }));
}

// A stateful stand-in for the auth settings and the MFA switch.
async function mockSettings(page: Page) {
  const state: Record<string, unknown> = { ...SETTINGS, mfaEnabled: false };
  const calls: string[] = [];
  await page.route('**/api/projects/test-project/auth-settings**', (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace('/api/projects/test-project/auth-settings', '') || '/';
    if (request.method() === 'PUT' && path === '/mfa') {
      calls.push(`PUT /mfa ${request.postData()}`);
      state.mfaEnabled = JSON.parse(request.postData() ?? '{}').enabled;
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(state) });
  });
  return calls;
}

test.describe('End-user MFA', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockProjectRole(page, 'admin');
  });

  test('hidden while the authmfa feature is off', async ({ page }) => {
    await mockFeatures(page, { authguest: true });
    const calls = await mockSettings(page);
    await page.goto('/project/test-project/auth/settings');
    await expect(page.getByTestId('guest-sign-in-card')).toBeVisible();
    await expect(page.getByTestId('mfa-card')).toHaveCount(0);
    expect(calls).toEqual([]);
  });

  test('an admin switches MFA on', async ({ page }) => {
    await mockFeatures(page, { authmfa: true });
    const calls = await mockSettings(page);
    await page.goto('/project/test-project/auth/settings');
    const card = page.getByTestId('mfa-card');
    await expect(card).toContainText('x-excalibase-aal');
    const toggle = card.getByLabel('Authenticator apps (TOTP)');
    await expect(toggle).not.toBeChecked();
    // The box follows the saved answer, so it turns on once the PUT returns.
    await toggle.click();
    await expect(toggle).toBeChecked();
    expect(calls).toEqual(['PUT /mfa {"enabled":true}']);
  });

  test("an admin resets an end user's MFA after confirming", async ({ page }) => {
    await mockFeatures(page, { authmfa: true });
    const deletes: string[] = [];
    await page.route('**/api/projects/test-project/end-users/**', (route) => {
      const request = route.request();
      if (request.method() === 'DELETE') {
        deletes.push(new URL(request.url()).pathname);
        return route.fulfill({ status: 204 });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(END_USERS) });
    });
    await page.goto('/project/test-project/auth/users');
    const alice = page.getByTestId('end-user-1');
    await alice.getByRole('button', { name: 'Reset MFA of alice@test.com' }).click();
    await expect(page.getByTestId('confirm-modal')).toContainText('signed out everywhere');
    expect(deletes).toEqual([]);
    await page.getByTestId('modal-confirm').click();
    await expect.poll(() => deletes).toEqual(['/api/projects/test-project/end-users/1/factors']);
  });
});
