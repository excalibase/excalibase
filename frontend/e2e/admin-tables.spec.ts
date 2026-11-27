import { test, expect, type Page } from '@playwright/test';
import { loginAs, SHOTS_DIR } from './helpers';

// EXC-762 step 4: the admin console's lists behind studio_v2, driven against a
// stub that pages, sorts, searches and filters like the server does.

// The first render waits for the dev server to compile the admin chunks, which can take well over the 5 s default when the machine is busy.
const FIRST_RENDER = 20_000;
const ADMIN = { id: 'boss', username: 'boss', email: 'boss@test.com', role: 'platform_admin' };
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

const PROJECTS = Array.from({ length: 90 }, (_, i) => ({
  projectId: `proj-${String(i).padStart(2, '0')}`,
  projectName: `Project ${String(i).padStart(2, '0')}`,
  orgId: 'o1', orgName: 'Acme', ownerId: `owner-${i % 9}`, ownerEmail: `dev${i % 9}@acme.test`,
  ownerStatus: i % 2 ? 'suspended' : 'active', ownerSuspended: i % 2 === 1,
  status: i % 10 === 0 ? 'FAILED' : 'ACTIVE', tier: i % 3 === 0 ? 'STANDARD' : 'FREE',
  createdAt: new Date(Date.UTC(2026, 10, 1, i)).toISOString(), deletionProtection: false,
}));

type Project = (typeof PROJECTS)[number];
const SORT_KEY: Record<string, (p: Project) => string> = {
  name: (p) => p.projectName, org: (p) => p.orgName, owner: (p) => p.ownerEmail, plan: (p) => p.tier, status: (p) => p.status, created: (p) => p.createdAt,
};

async function stubAdmin(page: Page, role = ADMIN.role) {
  const requests: URLSearchParams[] = [];
  const drops: { path: string; body: unknown }[] = [];
  await loginAs(page, { ...ADMIN, role });
  await page.route('**/api/config', (route) => route.fulfill(json({ deploymentMode: 'cloud', features: { studio_v2: true } })));
  await page.route('**/api/orgs', (route) => route.fulfill(json([])));
  await page.route('**/api/provision', (route) => route.fulfill(json([])));
  await page.route('**/api/capacity', (route) => route.fulfill(json({ usableCpuMilli: 4000, requestedCpuMilli: 1000, usableMemBytes: 8e9, requestedMemBytes: 2e9, headroomPercent: 20, projects: { total: 90, byTier: {} }, tiers: {} })));
  await page.route('**/api/admin/console/overview', (route) => route.fulfill(json({ newlySuspended: [], signups24h: 0, signupDomains: [], failedProjects: [], topCpu: [], topMemory: [], topEgress: [] })));
  await page.route('**/api/admin/projects?**', (route) => {
    const params = new URL(route.request().url()).searchParams;
    requests.push(params);
    const q = (params.get('q') ?? '').toLowerCase();
    let rows = PROJECTS.filter((p) => (!q || `${p.projectName} ${p.projectId} ${p.ownerEmail}`.toLowerCase().includes(q))
      && (!params.get('plan') || p.tier === params.get('plan')) && (!params.get('status') || p.status === params.get('status'))
      && (!params.get('owner') || (params.get('owner') === 'suspended') === p.ownerSuspended));
    const key = SORT_KEY[params.get('sort') ?? 'created'];
    const sign = (params.get('order') ?? (params.get('sort') ? 'asc' : 'desc')) === 'asc' ? 1 : -1;
    rows = [...rows].sort((a, b) => sign * key(a).localeCompare(key(b)));
    const offset = Number(params.get('offset') ?? 0);
    return route.fulfill(json({ items: rows.slice(offset, offset + Number(params.get('limit') ?? 25)), total: rows.length, limit: 25, offset }));
  });
  await page.route('**/api/admin/projects/*', (route) => {
    if (route.request().method() === 'DELETE') {
      drops.push({ path: new URL(route.request().url()).pathname, body: route.request().postDataJSON() });
      return route.fulfill(json({ status: 'DELETING' }, 202));
    }
    return route.fulfill(json({}, 404));
  });
  return { requests, drops };
}

test.describe('admin console tables (studio_v2)', () => {
  test('projects: 25 of 90, then search, filter, sort and page on the server', async ({ page }) => {
    const { requests } = await stubAdmin(page);
    await page.goto('/admin?tab=projects');
    const table = page.getByTestId('projects-admin-table');
    await expect(table.getByText('1–25 of 90')).toBeVisible({ timeout: FIRST_RENDER });
    await expect(table.getByRole('row')).toHaveCount(26);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-projects-90.png`, fullPage: true });

    await table.getByRole('button', { name: 'Next page' }).click();
    await expect(table.getByText('26–50 of 90')).toBeVisible();
    await expect.poll(() => requests.at(-1)?.get('offset')).toBe('25');

    await table.getByRole('combobox', { name: 'Plan' }).selectOption('STANDARD');
    await expect(table.getByText('1–25 of 30')).toBeVisible();
    await expect.poll(() => requests.at(-1)?.get('plan')).toBe('STANDARD');
    await expect.poll(() => requests.at(-1)?.get('offset')).toBe('0');

    await table.getByRole('combobox', { name: 'Owner' }).selectOption('suspended');
    await expect(table.getByText(/of 15$/)).toBeVisible();

    await table.getByRole('searchbox', { name: /filter projects/i }).fill('project 03');
    await expect(table.getByText('1–1 of 1')).toBeVisible();
    await expect(table.getByRole('cell', { name: /^Project 03 proj-03/ })).toBeVisible();
    await expect.poll(() => requests.at(-1)?.get('q')).toBe('project 03');

    await table.getByRole('searchbox', { name: /filter projects/i }).fill('');
    await table.getByRole('button', { name: 'Remove filter Plan: Standard' }).click();
    await table.getByRole('button', { name: 'Remove filter Owner: Suspended' }).click();
    await table.getByRole('button', { name: 'Project', exact: true }).click();
    await expect(table.getByRole('row').nth(1)).toContainText('Project 00');
    await table.getByRole('button', { name: 'Project', exact: true }).click();
    await expect(table.getByRole('row').nth(1)).toContainText('Project 89');
    await expect.poll(() => requests.at(-1)?.get('sort')).toBe('name');
    await expect.poll(() => requests.at(-1)?.get('order')).toBe('desc');
  });

  test('a bulk delete will not go until a reason is written, and sends it', async ({ page }) => {
    const { drops } = await stubAdmin(page);
    await page.goto('/admin?tab=projects');
    const table = page.getByTestId('projects-admin-table');
    await table.getByRole('checkbox', { name: 'Select Project 89' }).check({ timeout: FIRST_RENDER });
    await table.getByRole('checkbox', { name: 'Select Project 88' }).check();
    await expect(table.getByText('2 selected')).toBeVisible();
    await table.getByRole('button', { name: 'Delete…' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('button', { name: 'Delete' })).toBeDisabled();
    await dialog.getByLabel('Reason').fill('short');
    await expect(dialog.getByRole('button', { name: 'Delete' })).toBeDisabled();
    await dialog.getByLabel('Reason').fill('abandoned load-test projects');
    await dialog.getByRole('button', { name: 'Delete' }).click();

    await expect(page.getByText('Deleted 2 of 2 projects.')).toBeVisible();
    expect(drops.map((d) => d.path).sort()).toEqual(['/api/admin/projects/proj-88', '/api/admin/projects/proj-89']);
    expect(drops.every((d) => (d.body as { reason: string }).reason === 'abandoned load-test projects')).toBe(true);
  });

  test('the admin sections are tabs and users page on the server', async ({ page }) => {
    await stubAdmin(page);
    const users = Array.from({ length: 60 }, (_, i) => ({
      id: `u${i}`, email: `user${String(i).padStart(2, '0')}@acme.test`, username: `u${i}`, emailVerified: true, role: 'user', status: 'active', orgCount: 1, projectCount: 1, runningApps: 0,
    }));
    await page.route('**/api/admin/console/users?**', (route) => {
      const params = new URL(route.request().url()).searchParams;
      const offset = Number(params.get('offset') ?? 0);
      return route.fulfill(json({ items: users.slice(offset, offset + Number(params.get('limit') ?? 25)), total: users.length }));
    });
    await page.goto('/admin');
    await expect(page.getByRole('tab', { name: 'Needs attention' })).toHaveAttribute('aria-selected', 'true', { timeout: FIRST_RENDER });
    await page.getByRole('tab', { name: 'Users' }).click();
    const table = page.getByTestId('users-table');
    await expect(table.getByText('1–25 of 60')).toBeVisible({ timeout: FIRST_RENDER });
    await table.getByRole('button', { name: 'Next page' }).click();
    await expect(table.getByText('26–50 of 60')).toBeVisible();
    await expect(table.getByText('user25@acme.test')).toBeVisible();
  });

  test('a signed-in customer cannot reach /admin', async ({ page }) => {
    await stubAdmin(page, 'user');
    await page.goto('/admin');
    await expect(page).not.toHaveURL(/\/admin/, { timeout: FIRST_RENDER });
    await expect(page.getByTestId('projects-admin-table')).toHaveCount(0);
    await expect(page.getByRole('tab', { name: 'Projects' })).toHaveCount(0);
  });

  test('the table fits a phone: no sideways scroll on the page', async ({ page }) => {
    await stubAdmin(page);
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto('/admin?tab=projects');
    await expect(page.getByTestId('projects-admin-table').getByText('1–25 of 90')).toBeVisible({ timeout: FIRST_RENDER });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-projects-phone.png` });
  });
});
