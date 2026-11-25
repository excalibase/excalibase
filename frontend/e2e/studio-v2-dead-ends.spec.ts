import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-852: the console layout (studio_v2 on) has no dead ends.

const ADMIN = { id: '1', username: 'admin', email: 'admin@test.com', role: 'platform_admin' };
const ORG = { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' };

async function mockConsole(page: Page, orgs: unknown[], features: Record<string, boolean> = {}) {
  await page.route('**/api/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'cloud', sites: false, features: { studio_v2: true, ...features } }) }));
  await page.route(/\/api\/orgs(\?.*)?$/, (route) =>
    route.request().method() === 'GET'
      ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(orgs) })
      : route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ ...ORG, id: 'org-new', name: 'Fresh', slug: 'fresh' }) }));
  await page.route('**/api/provision', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }));
}

test.describe('Studio v2 dead ends', () => {
  test('the nav reaches the organizations list, the switcher offers create, the bell is gone', async ({ page }) => {
    await loginAs(page);
    await mockConsole(page, [ORG]);
    await page.goto('/');
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Organizations' }).click();
    await expect(page).toHaveURL(/\/orgs$/);
    await expect(page.getByRole('heading', { name: 'Organizations' })).toBeVisible();
    await page.getByRole('button', { name: /organization: acme/i }).click();
    await page.getByRole('link', { name: /create organization/i }).click();
    await expect(page).toHaveURL(/\/orgs\?new=1$/);
    await expect(page.getByText('Create Organization')).toBeVisible();
    await expect(page.getByRole('button', { name: /notifications/i })).toHaveCount(0);
  });

  test('a person with no organization can create one from the first page', async ({ page }) => {
    await loginAs(page);
    await mockConsole(page, []);
    await page.goto('/');
    await expect(page.getByRole('heading', { name: /join or create an organization/i })).toBeVisible();
    await page.getByRole('link', { name: /create an organization/i }).click();
    await page.getByPlaceholder('My Company').fill('Fresh');
    await page.getByRole('button', { name: 'Create', exact: true }).click();
    await page.waitForURL('**/orgs/org-new');
  });

  test('a person with no organization can accept an invite', async ({ page }) => {
    await loginAs(page);
    await mockConsole(page, []);
    let accepted: unknown;
    await page.route('**/api/orgs/invites/accept', (route) => {
      accepted = route.request().postDataJSON();
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });
    await page.goto('/');
    await page.getByLabel('Invite link or code').fill('http://localhost/login?invite=tok-1');
    await page.getByRole('button', { name: 'Accept invite' }).click();
    await page.waitForURL('**/orgs');
    expect(accepted).toEqual({ token: 'tok-1' });
  });

  test('an admin gets the Host link where host monitoring is on', async ({ page }) => {
    await loginAs(page, ADMIN);
    await mockConsole(page, [ORG], { hostmetrics: true });
    await page.goto('/');
    await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Host' })).toHaveAttribute('href', '/admin/host');
  });

  test('project tabs are named for the pages they open', async ({ page }) => {
    await loginAs(page);
    await mockConsole(page, [ORG]);
    await page.goto('/project/p1');
    const tabs = page.getByRole('navigation', { name: 'Project' });
    await expect(tabs.getByRole('link', { name: 'Metrics' })).toHaveAttribute('href', '/project/p1/monitoring/metrics');
    await expect(tabs.getByRole('link', { name: 'Logs' })).toHaveAttribute('href', '/project/p1/monitoring/logs');
    await expect(tabs.getByRole('link', { name: 'Usage' })).toHaveCount(0);
  });
});
