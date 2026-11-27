import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { loginAs, mockCloudMode } from './helpers';

// A cold dev server compiles on the first visit.
const FIRST_RENDER = 20_000;
const ADMIN = { id: 'boss', username: 'boss', email: 'boss@test.com', role: 'platform_admin' };

const user = (over: object) => ({
  id: 'u1', email: 'dev@acme.test', username: 'dev', emailVerified: true, role: 'user', status: 'active',
  orgCount: 1, projectCount: 1, runningApps: 1, tempMail: false, lastLoginIp: '203.0.113.5', ...over,
});

const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

// The console is driven against stubbed endpoints: the journey is the screen's
// own behaviour (search, reason, status flips), not the platform's.
async function stubConsole(page: Page) {
  const state = { users: [user({}), user({ id: 'u2', email: 'bad@mailinator.test', status: 'suspended', suspendedReason: 'proxy abuse', tempMail: true, runningApps: 0 })] };
  const posts: { url: string; body: unknown }[] = [];

  await page.route('**/api/capacity', (route) => route.fulfill(json({ usableCpuMilli: 4000, requestedCpuMilli: 1000, usableMemBytes: 8e9, requestedMemBytes: 2e9, headroomPercent: 20, projects: { total: 1, byTier: {} }, tiers: {} })));
  await page.route('**/api/admin/projects?**', (route) => route.fulfill(json({ items: [{ projectId: 'p1', projectName: 'shop', orgId: 'o1', orgName: 'Acme', ownerEmail: 'dev@acme.test', ownerStatus: 'active', status: 'ACTIVE', tier: 'free' }], total: 1 })));
  await page.route('**/api/admin/storage', (route) => route.fulfill(json({ enabled: false })));
  await page.route('**/api/admin/tiers/**', (route) => route.fulfill(json([])));
  await page.route('**/api/admin/sso-providers**', (route) => route.fulfill(json([])));
  await page.route('**/api/admin/console/overview', (route) => route.fulfill(json({
    newlySuspended: [{ userId: 'u2', email: 'bad@mailinator.test', reason: 'proxy abuse', suspendedAt: '2026-11-16T01:00:00Z' }],
    signups24h: 12, signupDomains: [{ domain: 'mailinator.test', count: 9 }], failedProjects: [], topCpu: [], topMemory: [], topEgress: [],
  })));
  await page.route('**/api/admin/console/orgs**', (route) => route.fulfill(json({ items: [], total: 0 })));
  await page.route('**/api/admin/console/users?**', (route) => {
    const url = new URL(route.request().url());
    const query = url.searchParams.get('q') ?? '';
    const withSuspended = url.searchParams.get('includeSuspended') === 'true';
    const items = state.users.filter((u) => (withSuspended || u.status !== 'suspended') && u.email.includes(query));
    return route.fulfill(json({ items, total: items.length }));
  });
  await page.route('**/api/admin/console/users/*/*', (route) => {
    const [, userId, action] = new URL(route.request().url()).pathname.match(/users\/([^/]+)\/(suspend|unsuspend)$/) ?? [];
    posts.push({ url: new URL(route.request().url()).pathname, body: route.request().postDataJSON() });
    const target = state.users.find((u) => u.id === userId);
    if (target) target.status = action === 'suspend' ? 'suspended' : 'active';
    return route.fulfill(json({ userId, status: target?.status, appsStopped: [], appsRestored: [], appsFailed: [] }));
  });
  return posts;
}

test.describe('admin console', () => {
  test('find a suspended user by email, unsuspend them, then suspend them again', async ({ page }) => {
    await loginAs(page, ADMIN);
    await mockCloudMode(page);
    const posts = await stubConsole(page);
    await page.goto('/admin');

    await expect(page.getByText('12 sign-ups in the last 24 h')).toBeVisible({ timeout: FIRST_RENDER });

    await page.getByRole('tab', { name: 'Users' }).click();
    const users = page.getByTestId('users-table');
    await users.getByRole('searchbox', { name: 'Filter users' }).fill('mailinator');

    const row = users.getByRole('row', { name: /bad@mailinator.test/ });
    await expect(row.getByText('Suspended')).toBeVisible();
    await expect(row.getByText('temp mail')).toBeVisible();

    await row.getByRole('button', { name: 'Unsuspend' }).click();
    const unsuspendDialog = page.getByRole('dialog');
    await expect(unsuspendDialog.getByRole('button', { name: 'Unsuspend' })).toBeDisabled();
    await unsuspendDialog.getByLabel('Reason').fill('appealed, verified by email');
    await unsuspendDialog.getByRole('button', { name: 'Unsuspend' }).click();

    await expect(row.getByText('Active')).toBeVisible();
    expect(posts[0]).toEqual({ url: '/api/admin/console/users/u2/unsuspend', body: { reason: 'appealed, verified by email' } });

    await row.getByRole('button', { name: 'Suspend' }).click();
    const suspendDialog = page.getByRole('dialog');
    await suspendDialog.getByLabel('Reason').fill('abuse again, proxy traffic');
    await suspendDialog.getByRole('button', { name: 'Suspend' }).click();

    await expect(row.getByText('Suspended')).toBeVisible();
    expect(posts[1]).toEqual({ url: '/api/admin/console/users/u2/suspend', body: { reason: 'abuse again, proxy traffic', reasonCode: 'other' } });

    // The rule the admin picks is what the account holder is told, so it is sent with the reason.
    await row.getByRole('button', { name: 'Unsuspend' }).click();
    await page.getByRole('dialog').getByLabel('Reason').fill('appealed again, cleared');
    await page.getByRole('dialog').getByRole('button', { name: 'Unsuspend' }).click();
    await expect(row.getByText('Active')).toBeVisible();

    await row.getByRole('button', { name: 'Suspend' }).click();
    const pickDialog = page.getByRole('dialog');
    await pickDialog.getByLabel(/Rule broken/).selectOption('crypto_mining');
    await pickDialog.getByLabel('Reason').fill('miner found in the image');
    await pickDialog.getByRole('button', { name: 'Suspend' }).click();

    await expect(row.getByText('Suspended')).toBeVisible();
    expect(posts[3]).toEqual({ url: '/api/admin/console/users/u2/suspend', body: { reason: 'miner found in the image', reasonCode: 'crypto_mining' } });
  });

  test('a platform admin outside /admin sees only their own orgs', async ({ page }) => {
    await loginAs(page, ADMIN);
    await mockCloudMode(page);
    await page.route('**/api/orgs', (route) => route.fulfill(json([{ id: 'mine', name: 'Mine', slug: 'mine', role: 'owner', tier: 'free' }])));
    await page.route('**/api/provision', (route) => route.fulfill(json([])));
    await page.goto('/orgs');
    await expect(page.getByText('Mine').first()).toBeVisible();
    await expect(page.getByText('acme')).toHaveCount(0);
  });
});
