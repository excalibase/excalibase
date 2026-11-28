import { test, expect, type Page } from '@playwright/test';
import { loginAs, SHOTS_DIR } from './helpers';

// EXC-856: the admin console's account page and audit log. The server is stubbed;
// the audit list answers by keyset cursor, like the real one.

const ADMIN = { id: 'boss', username: 'boss', email: 'boss@test.com', role: 'platform_admin' };
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
const WHEN = new Date(Date.now() - 3_600_000).toISOString();
const TOTAL = 60;
const PAGE = 25;

const audit = Array.from({ length: TOTAL }, (_, i) => ({
  id: String(TOTAL - i), at: WHEN, actorId: 'boss', actorEmail: 'boss@test.com',
  action: i % 3 === 0 ? 'user.suspend' : 'admin.project.open', resource: i % 3 === 0 ? 'user' : 'project',
  resourceId: i % 3 === 0 ? 'u1' : `p${i}`, targetEmail: i % 3 === 0 ? 'cust@example.test' : undefined, details: `reason number ${i}`,
}));

const account = (suspended: boolean) => ({
  profile: { id: 'u1', email: 'cust@example.test', username: 'cust', role: 'user', status: suspended ? 'suspended' : 'active', emailVerified: true, emailVerifiedAt: WHEN, signedUpAt: WHEN, lastLoginAt: WHEN, lastLoginIp: '203.0.113.9',
    trust: { state: 'limited', method: 'github', reason: 'account too young', decidedAt: WHEN } },
  suspension: suspended ? { suspended: true, reason: 'proxy node', reasonCode: 'proxy', suspendedAt: WHEN } : { suspended: false },
  suspensions: [{ action: 'user.suspend', actorEmail: 'boss@test.com', reason: 'proxy node', at: WHEN }],
  trustHistory: [{ to: 'limited', method: 'github', reason: 'account too young', actor: 'system', at: WHEN }],
  orgs: [{ id: 'o1', name: 'Cust Co', slug: 'cust-co', tier: 'FREE', role: 'owner', owner: true }],
  projects: [{ projectId: 'p1', name: 'shop', orgId: 'o1', orgName: 'Cust Co', status: 'ACTIVE' }],
  signIn: [{ kind: 'provider', provider: 'github', email: 'cust@example.test', linkedAt: WHEN }],
  audit: audit.filter((row) => row.resourceId === 'u1').slice(0, 5),
});

async function stub(page: Page, state = { suspended: false }) {
  const asked: Record<string, string | null>[] = [];
  const posted: { url: string; body: unknown }[] = [];
  await loginAs(page, ADMIN);
  await page.route('**/api/config', (route) => route.fulfill(json({ deploymentMode: 'cloud', features: { studio_v2: true } })));
  await page.route('**/api/orgs', (route) => route.fulfill(json([])));
  await page.route('**/api/provision', (route) => route.fulfill(json([])));
  await page.route('**/api/admin/console/overview', (route) => route.fulfill(json({ newlySuspended: [], signups24h: 0, signupDomains: [], failedProjects: [], topCpu: [], topMemory: [], topEgress: [] })));
  await page.route('**/api/admin/attention/**', (route) => route.fulfill(json({ items: [], total: 0 })));
  await page.route('**/api/admin/accounts/u1', (route) => route.fulfill(json(account(state.suspended))));
  await page.route('**/api/admin/console/users/u1/*', async (route) => {
    posted.push({ url: new URL(route.request().url()).pathname, body: route.request().postDataJSON() });
    state.suspended = route.request().url().endsWith('/suspend');
    return route.fulfill(json({ userId: 'u1', status: state.suspended ? 'suspended' : 'active', appsStopped: [], appsRestored: [], appsFailed: [] }));
  });
  await page.route('**/api/admin/console/users?**', (route) => route.fulfill(json({ items: [
    { id: 'u1', email: 'cust@example.test', username: 'cust', emailVerified: true, role: 'user', status: 'active', orgCount: 1, projectCount: 1, runningApps: 0 },
  ], total: 1 })));
  await page.route('**/api/admin/audit/**', async (route) => {
    const params = new URL(route.request().url()).searchParams;
    asked.push(Object.fromEntries(params.entries()));
    const action = params.get('action') ?? '';
    const rows = audit.filter((row) => row.action.startsWith(action));
    const before = params.get('cursor') ? Number(params.get('cursor')) : Number.POSITIVE_INFINITY;
    const after = rows.filter((row) => Number(row.id) < before);
    const items = after.slice(0, PAGE);
    const next = after.length > PAGE ? items.at(-1)?.id : undefined;
    return route.fulfill(json({ items, nextCursor: next }));
  });
  return { asked, posted };
}

test.describe('admin account page', () => {
  test('opens from the users table and shows the whole account; suspending asks for a reason first', async ({ page }) => {
    const { posted } = await stub(page);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/admin?tab=users');
    await page.getByRole('link', { name: 'cust@example.test' }).click();
    await expect(page).toHaveURL(/\/admin\/users\/u1$/);
    await expect(page.getByRole('heading', { name: 'cust@example.test' })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Trust' })).toContainText('Limited');
    await expect(page.getByRole('region', { name: 'Organizations' })).toContainText('Cust Co');
    await expect(page.getByRole('region', { name: 'Projects' })).toContainText('shop');
    await expect(page.getByRole('region', { name: 'Sign-in methods' })).toContainText('github');
    await page.screenshot({ path: `${SHOTS_DIR}/admin-user-detail-1280.png`, fullPage: true });

    await page.getByRole('button', { name: 'Suspend account' }).click();
    const dialog = page.getByRole('dialog', { name: 'Suspend cust@example.test' });
    await expect(dialog).toBeVisible();
    expect(posted).toHaveLength(0);
    await expect(dialog.getByRole('button', { name: 'Suspend', exact: true })).toBeDisabled();
    await dialog.getByRole('textbox', { name: 'Reason' }).fill('running a proxy node');
    await dialog.getByRole('button', { name: 'Suspend', exact: true }).click();
    await expect(page.getByText(/Account is now suspended/)).toBeVisible();
    expect(posted).toEqual([{ url: '/api/admin/console/users/u1/suspend', body: { reason: 'running a proxy node', reasonCode: 'other' } }]);
    await expect(page.getByRole('button', { name: 'Unsuspend account' })).toBeVisible();
  });

  test('on a phone the cards stack with no sideways scroll', async ({ page }) => {
    await stub(page, { suspended: true });
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto('/admin/users/u1');
    await expect(page.getByRole('heading', { name: 'cust@example.test' })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-user-detail-phone.png`, fullPage: true });
  });
});

test.describe('admin audit log', () => {
  const rows = (page: Page) => page.getByRole('table', { name: 'Audit log' }).getByRole('row').filter({ hasText: 'boss@test.com' });

  test('pages by cursor to the end, and a filter starts again from the newest', async ({ page }) => {
    const { asked } = await stub(page);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/admin?tab=audit');
    await expect(page.getByRole('tab', { name: 'Audit log' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByRole('button', { name: 'Load more' })).toBeVisible();
    await expect(rows(page)).toHaveCount(PAGE);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-audit-1280.png`, fullPage: true });
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(rows(page)).toHaveCount(2 * PAGE);
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(rows(page)).toHaveCount(TOTAL);
    await expect(page.getByText("That's all (60)")).toBeVisible();
    expect(asked.map((q) => q.cursor ?? null)).toEqual([null, '36', '11']);

    await page.getByRole('textbox', { name: 'Action' }).fill('user.');
    await expect(rows(page)).toHaveCount(20);
    expect(asked.at(-1)).toMatchObject({ action: 'user.' });
    expect(asked.at(-1)?.cursor).toBeUndefined();
    await page.screenshot({ path: `${SHOTS_DIR}/admin-audit-filtered-1280.png`, fullPage: true });
  });

  test('on a phone the log has no sideways page scroll', async ({ page }) => {
    await stub(page);
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto('/admin?tab=audit');
    await expect(rows(page)).toHaveCount(PAGE);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-audit-phone.png`, fullPage: true });
  });
});
