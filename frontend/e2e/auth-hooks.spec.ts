import { test, expect, type Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints, mockProjectRole } from './helpers';

const SECRET = `whsec_${'fixture'.repeat(6)}`;
const NAMES = ['custom_access_token', 'before_user_created', 'send_email'];

async function mockFeatures(page: Page, features: Record<string, boolean>) {
  await page.route('**/api/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'selfhosted', features }) }));
  await page.route('**/api/projects/test-project/auth-settings*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ siteUrl: 'https://app.example.com', requireEmailVerification: false }) }));
}

// A stateful stand-in for the four auth-hooks routes.
async function mockHooks(page: Page) {
  const state = { secretSet: false, hooks: NAMES.map((hook) => ({ hook, enabled: false, url: '' })) };
  const calls: string[] = [];
  await page.route('**/api/projects/test-project/auth-hooks**', (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace('/api/projects/test-project/auth-hooks', '');
    calls.push(`${request.method()} ${path || '/'}`);
    const reply = (body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (request.method() === 'POST' && path === '/secret') {
      state.secretSet = true;
      return reply({ secret: SECRET });
    }
    if (request.method() === 'PUT') {
      const body = JSON.parse(request.postData() ?? '{}');
      if (body.enabled && !state.secretSet) return reply({ error: 'generate a signing secret before enabling a hook' }, 409);
      state.hooks = state.hooks.map((hook) => (`/${hook.hook}` === path ? { ...hook, ...body } : hook));
    }
    return reply(state);
  });
  return calls;
}

test.describe('Auth hooks', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockProjectRole(page, 'admin');
  });

  test('hidden while the authhooks feature is off', async ({ page }) => {
    await mockFeatures(page, { authguest: true });
    const calls = await mockHooks(page);
    await page.goto('/project/test-project/auth/settings');
    await expect(page.getByTestId('auth-settings-page')).toBeVisible();
    await expect(page.getByTestId('guest-sign-in-card')).toBeVisible();
    await expect(page.getByTestId('auth-hooks-card')).toHaveCount(0);
    expect(calls).toEqual([]);
  });

  test('an admin generates the secret once, then switches a hook on', async ({ page }) => {
    await mockFeatures(page, { authhooks: true });
    const calls = await mockHooks(page);
    await page.goto('/project/test-project/auth/settings');
    const card = page.getByTestId('auth-hooks-card');
    await expect(card).toContainText('webhook-signature');
    await expect(page.getByTestId('auth-hooks-secret-state')).toHaveText('Not set');
    await expect(page.getByLabel('Enable Send email')).toBeDisabled();

    await page.getByRole('button', { name: 'Generate secret' }).click();
    await expect(page.getByTestId('auth-hooks-new-secret')).toContainText(SECRET);
    await expect(page.getByTestId('auth-hooks-new-secret')).toContainText('not shown again');
    await expect(page.getByTestId('auth-hooks-secret-state')).toHaveText('Set');

    const sendEmail = page.getByTestId('auth-hook-send_email');
    await sendEmail.getByLabel('Enable Send email').check();
    await sendEmail.getByLabel('Send email URL').fill('https://mail.example.com/send');
    await sendEmail.getByRole('button', { name: 'Save Send email' }).click();
    await expect(sendEmail).toContainText('Saved');
    expect(calls).toEqual(['GET /', 'POST /secret', 'PUT /send_email']);
  });
});
