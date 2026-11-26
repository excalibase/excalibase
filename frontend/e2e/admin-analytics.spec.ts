import { test, expect, type Page } from '@playwright/test';
import { loginAs, SHOTS_DIR } from './helpers';

// EXC-898: the analytics tabs share the admin console's tab bar and exist only
// while FEATURE_ADMINANALYTICS is on. The numbers are stubbed.

const ADMIN = { id: 'boss', username: 'boss', email: 'boss@test.com', role: 'platform_admin' };
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

const day = (i: number) => `2026-11-${String(i + 1).padStart(2, '0')}`;
const SIGNUPS = [0, 0, 1, 0, 21, 0, 1, 0, 38, 31, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 3, 8, 5, 4, 0, 7, 42, 91, 16, 7];
const SUSPENDED = [0, 0, 0, 0, 17, 0, 1, 0, 37, 27, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 3, 5, 1, 0, 0, 0, 0, 2, 0, 0];

const USERS = {
  windowDays: 30, signups: 292, suspended: 96, activeLast7Days: 154,
  perDay: SIGNUPS.map((total, i) => ({ day: day(i), total, flagged: SUSPENDED[i] })),
  funnel: [{ label: 'Signed up', count: 292 }, { label: 'Verified email', count: 182 }, { label: 'Created a project', count: 128 }, { label: 'Never refused', count: 98 }],
  domains: [{ domain: 'gmail.com', accounts: 146, suspended: 24 }, { domain: 'fldubai.com', accounts: 29, suspended: 29 }, { domain: 'muse.ai', accounts: 12, suspended: 0 }],
};
const ABUSE = {
  windowDays: 30, proxyRefusals: 140, registryRefusals: 9, refusedUsers: 99, refusedStillActive: 98, suspended: 96,
  perDay: SIGNUPS.map((_, i) => ({ day: day(i), total: i > 21 ? [5, 0, 1, 4, 23, 78, 19, 19][i - 22] ?? 0 : 0, flagged: i > 21 ? [3, 0, 1, 4, 21, 54, 12, 6][i - 22] ?? 0 : 0 })),
  latest: [{ at: '2026-11-25T15:15:00Z', projectId: 'proj-7y14t3tmyi', userId: 'u9', email: 'builder@example.test', kind: 'proxy', details: 'surface=create name="web" image="registry.sslip.io/x"', suspended: false }],
};
const TRAFFIC = { windowDays: 7, peakConnections: 35, peakConnectionsPerDay: [21, 22, 23, 24, 25, 26, 27].map((i) => ({ day: day(i), total: 30 + (i % 5) })) };

async function stubAdmin(page: Page, analytics: boolean) {
  const analyticsCalls: string[] = [];
  await loginAs(page, ADMIN);
  await page.route('**/api/config', (route) => route.fulfill(json({ deploymentMode: 'cloud', features: { adminanalytics: analytics } })));
  await page.route('**/api/orgs', (route) => route.fulfill(json([])));
  await page.route('**/api/provision', (route) => route.fulfill(json([])));
  await page.route('**/api/admin/console/overview', (route) => route.fulfill(json({ newlySuspended: [], signups24h: 0, signupDomains: [], failedProjects: [], topCpu: [], topMemory: [], topEgress: [] })));
  await page.route('**/api/admin/analytics/*', (route) => {
    const name = new URL(route.request().url()).pathname.split('/').pop() ?? '';
    analyticsCalls.push(name);
    return route.fulfill(json({ users: USERS, abuse: ABUSE, traffic: TRAFFIC }[name] ?? {}, name in { users: 1, abuse: 1, traffic: 1 } ? 200 : 404));
  });
  return analyticsCalls;
}

test.describe('admin analytics tabs', () => {
  test('with the flag on the analytics tabs join the same bar and show real numbers', async ({ page }) => {
    await stubAdmin(page, true);
    await page.goto('/admin');
    await expect(page.getByRole('tab', { name: /Sign-ups\s*292/ })).toBeVisible();
    await expect(page.getByRole('tab', { name: /Abuse\s*98/ })).toBeVisible();
    await page.setViewportSize({ width: 1280, height: 900 });
    const tabTops = await page.getByRole('tab').evaluateAll((els) => new Set(els.map((el) => Math.round(el.getBoundingClientRect().top))).size);
    expect(tabTops).toBe(1);
    await page.getByRole('tab', { name: /Sign-ups/ }).click();
    await expect(page.getByLabel('Needs attention').getByRole('button', { name: /98 accounts had a deploy refused/ })).toBeVisible();
    await expect(page.getByText('fldubai.com')).toBeVisible();
    await page.getByTestId('bar-hit-27').hover();
    await expect(page.getByRole('tooltip')).toContainText('91 sign-ups, 2 suspended');
    await page.screenshot({ path: `${SHOTS_DIR}/admin-analytics-users.png`, fullPage: true });

    await page.getByLabel('Needs attention').getByRole('button', { name: /98 accounts had a deploy refused/ }).click();
    await expect(page.getByRole('tab', { name: /Abuse/ })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByText('proj-7y14t3tmyi')).toBeVisible();
    await page.screenshot({ path: `${SHOTS_DIR}/admin-analytics-abuse.png`, fullPage: true });

    await page.getByRole('tab', { name: /Traffic/ }).click();
    await expect(page.getByRole('note')).toContainText('not collected yet');
    await page.screenshot({ path: `${SHOTS_DIR}/admin-analytics-traffic.png`, fullPage: true });

    await page.getByRole('tab', { name: /Storage/ }).click();
    await expect(page.getByRole('note')).toHaveCount(2);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-analytics-storage.png`, fullPage: true });
  });

  test('with the flag off there are no analytics tabs and nothing is requested', async ({ page }) => {
    const calls = await stubAdmin(page, false);
    await page.goto('/admin');
    await expect(page.getByRole('tab', { name: 'Needs attention' })).toBeVisible();
    await expect(page.getByRole('tab', { name: 'Platform' })).toBeVisible();
    await expect(page.getByRole('tab', { name: /Abuse|Traffic|Storage|Sign-ups/ })).toHaveCount(0);
    expect(calls).toEqual([]);
  });

  test('the tab bar and charts fit a phone without a sideways page scroll', async ({ page }) => {
    await stubAdmin(page, true);
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto('/admin?tab=analytics-users');
    await expect(page.getByText('fldubai.com')).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
    await page.screenshot({ path: `${SHOTS_DIR}/admin-analytics-phone.png`, fullPage: true });
  });
});
