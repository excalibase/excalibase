import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-853: the Account page and the project Variables page of the console layout (studio_v2 on).

const USER = { id: '1', username: 'dev', email: 'dev@test.com', role: 'user' };
const ORG = { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' };
const SECRET_PLAIN = 'plain-but-credential-shaped';

const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

async function mockConsole(page: Page, studioV2 = true) {
  await page.route('**/api/config', (route) => route.fulfill(json({ deploymentMode: 'cloud', sites: false, features: { studio_v2: studioV2 } })));
  await page.route(/\/api\/orgs(\?.*)?$/, (route) => route.fulfill(json([ORG])));
  await page.route('**/api/provision', (route) => route.fulfill(json([])));
  await page.route('**/api/auth/me', (route) => route.fulfill(json({ ...USER })));
}

test.describe('Studio v2 account and variables', () => {
  test('the account menu opens the Account page and the reset link goes out', async ({ page }) => {
    await loginAs(page, USER);
    await mockConsole(page);
    let reset: unknown;
    await page.route('**/api/email/reset/send', (route) => {
      reset = route.request().postDataJSON();
      return route.fulfill(json({ expiresInMinutes: 30 }));
    });
    await page.goto('/');
    await page.getByRole('button', { name: /account menu/i }).click();
    await page.getByRole('menuitem', { name: 'Account' }).click();
    await expect(page).toHaveURL(/\/account$/);
    await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible();
    await expect(page.getByText('dev@test.com').first()).toBeVisible();
    await page.getByRole('button', { name: /email me a reset link/i }).click();
    await expect(page.getByText(/expires in 30 minutes/i)).toBeVisible();
    expect(reset).toEqual({ email: 'dev@test.com' });
    await page.getByRole('link', { name: /manage access keys/i }).click();
    await expect(page).toHaveURL(/\/account\/tokens$/);
  });

  test('the Variables tab masks secrets and reveals or copies only on a click', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await loginAs(page, USER);
    await mockConsole(page);
    await page.route('**/api/projects/p1/apps/', (route) => route.fulfill(json([{
      id: 'a1', projectId: 'p1', name: 'api', image: 'x', port: 80, replicas: 1, tier: 'FREE', status: 'ACTIVE', version: 1, createdAt: '', updatedAt: '',
      env: [
        { name: 'API_BASE_URL', kind: 'literal', value: 'https://api.example.test' },
        { name: 'STRIPE_SECRET', kind: 'secret', secret: { path: 'p1/api', key: 'STRIPE_SECRET' } },
        { name: 'LEGACY_TOKEN', kind: 'literal', value: SECRET_PLAIN },
      ],
    }])));
    await page.goto('/project/p1');
    await page.getByRole('navigation', { name: 'Project' }).getByRole('link', { name: 'Variables' }).click();
    await expect(page).toHaveURL(/\/project\/p1\/variables$/);
    await expect(page.getByTestId('variable-API_BASE_URL')).toContainText('https://api.example.test');
    await expect(page.getByTestId('variable-STRIPE_SECRET')).toContainText('Set');
    await expect(page.getByTestId('variable-STRIPE_SECRET').getByRole('button')).toHaveCount(0);
    await expect(page.getByText(SECRET_PLAIN)).toHaveCount(0);
    await page.getByRole('button', { name: 'Reveal LEGACY_TOKEN' }).click();
    await expect(page.getByText(SECRET_PLAIN)).toBeVisible();
    await page.getByRole('button', { name: 'Copy API_BASE_URL' }).click();
    await expect(page.getByTestId('variable-API_BASE_URL').getByText('Copied')).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('https://api.example.test');
    expect(page.url()).not.toContain(SECRET_PLAIN);
  });

  test('with the flag off neither page is served', async ({ page }) => {
    await loginAs(page, USER);
    await mockConsole(page, false);
    await page.goto('/account');
    await expect(page.getByTestId('feature-unavailable')).toBeVisible();
    await page.goto('/project/p1/variables');
    await expect(page.getByTestId('feature-unavailable')).toBeVisible();
  });
});
