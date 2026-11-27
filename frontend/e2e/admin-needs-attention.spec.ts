import { test, expect, type Page } from '@playwright/test';
import { loginAs, SHOTS_DIR } from './helpers';

// EXC-898: the needs-attention columns scroll inside themselves and read their
// lists a page at a time. The server is stubbed with a keyset-style cursor.

const ADMIN = { id: 'boss', username: 'boss', email: 'boss@test.com', role: 'platform_admin' };
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
const TOTAL = 60;
const REASONS = ['Proxy apps refused: the app looked like a VPN or proxy node and was refused at deploy', 'Crypto mining found in the deployed image'];

const suspended = Array.from({ length: TOTAL }, (_, i) => ({
  userId: `u${i}`, email: `suspended-user-${String(i).padStart(2, '0')}@example.test`,
  reason: i < 45 ? REASONS[0] : i < 55 ? REASONS[1] : `Unique reason number ${i} written by an admin`,
  suspendedAt: new Date(Date.now() - i * 3 * 60_000).toISOString(),
}));
const failed = Array.from({ length: 5 }, (_, i) => ({ projectId: `p${i}`, projectName: `broken-${i}`, orgId: 'o1', ownerEmail: `o${i}@example.test`, ownerStatus: 'active', status: 'FAILED', failureReason: 'no capacity' }));

async function stub(page: Page) {
  const asked: (string | null)[] = [];
  await loginAs(page, ADMIN);
  await page.route('**/api/config', (route) => route.fulfill(json({ deploymentMode: 'cloud', features: {} })));
  await page.route('**/api/orgs', (route) => route.fulfill(json([])));
  await page.route('**/api/provision', (route) => route.fulfill(json([])));
  await page.route('**/api/admin/console/overview', (route) => route.fulfill(json({ newlySuspended: [], signups24h: 31, signupDomains: [{ domain: 'gmail.com', count: 20 }, { domain: 'proton.me', count: 11 }], failedProjects: [], topCpu: [], topMemory: [], topEgress: [] })));
  await page.route('**/api/admin/attention/failed-projects**', (route) => route.fulfill(json({ items: failed, total: failed.length })));
  await page.route('**/api/admin/attention/suspensions**', async (route) => {
    const params = new URL(route.request().url()).searchParams;
    asked.push(params.get('cursor'));
    const start = Number(params.get('cursor') ?? 0);
    const limit = Number(params.get('limit') ?? 20);
    const items = suspended.slice(start, start + limit);
    const next = start + limit < TOTAL ? String(start + limit) : undefined;
    await new Promise((resolve) => setTimeout(resolve, 150));
    return route.fulfill(json({ items, total: TOTAL, nextCursor: next }));
  });
  return asked;
}

const column = (page: Page) => page.getByRole('list', { name: 'Newly suspended' }).locator('xpath=..');
const pageHeight = (page: Page) => page.evaluate(() => document.documentElement.scrollHeight);

test.describe('needs attention columns', () => {
  test('60 suspensions: the page does not grow, scrolling the column loads more, and it ends', async ({ page }) => {
    const asked = await stub(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/admin');
    await expect(page.getByRole('heading', { name: 'Newly suspended · 60' })).toBeVisible();
    await expect(page.getByText(`${REASONS[0].slice(0, 20)}`).first()).toBeVisible();
    const rows = page.getByRole('button', { name: /suspended-user-/ });
    await expect(rows).toHaveCount(20);
    const heightBefore = await pageHeight(page);
    const box = await column(page).boundingBox();
    expect(box?.height).toBeLessThanOrEqual(22 * 16 + 40);
    await page.screenshot({ path: `${SHOTS_DIR}/needs-attention-1280.png`, fullPage: true });

    for (let i = 0; i < 6 && (await rows.count()) < TOTAL; i++) {
      await column(page).evaluate((el) => el.scrollTo(0, el.scrollHeight));
      await page.waitForTimeout(400);
    }
    await expect(rows).toHaveCount(TOTAL);
    await expect(page.getByText("That's all (60)")).toBeVisible();
    expect(await pageHeight(page)).toBe(heightBefore);
    expect(asked).toEqual([null, '20', '40']);
    await column(page).evaluate((el) => el.scrollTo(0, el.scrollHeight));
    await page.screenshot({ path: `${SHOTS_DIR}/needs-attention-1280-end.png`, fullPage: true });
  });

  test('a row opens to its full reason', async ({ page }) => {
    await stub(page);
    await page.goto('/admin');
    const row = page.getByRole('button', { name: /suspended-user-00/ });
    await row.click();
    await expect(row).toHaveAttribute('aria-expanded', 'true');
    await expect(page.getByRole('list', { name: 'Newly suspended' }).getByRole('paragraph')).toContainText('looked like a VPN or proxy node and was refused at deploy');
  });

  test('on a phone the columns stack, each capped, with no sideways scroll', async ({ page }) => {
    await stub(page);
    await page.setViewportSize({ width: 390, height: 800 });
    await page.goto('/admin');
    await expect(page.getByRole('heading', { name: 'Newly suspended · 60' })).toBeVisible();
    const box = await column(page).boundingBox();
    expect(box?.height).toBeLessThanOrEqual(22 * 16 + 40);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
    await page.screenshot({ path: `${SHOTS_DIR}/needs-attention-phone.png`, fullPage: true });
  });
});
