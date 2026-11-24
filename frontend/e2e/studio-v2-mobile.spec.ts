import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-851: the v2 project shell on a phone, behind studio_v2.

test.use({ viewport: { width: 390, height: 844 } });

const SHOTS = process.env.EXC851_SHOTS;
const INTERNAL_HOST = 'p1-postgres-rw.org-1.svc.cluster.local';
const NOW = '2026-10-01T00:00:00Z';

const PROJECT = {
  projectId: 'p1', projectName: 'p1', orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE',
  status: 'ACTIVE', currentStage: 'COMPLETED', port: 5432, databaseName: 'app', username: 'excalibase_app',
  backupEnabled: true, backupSchedule: '0 2 * * *', createdAt: NOW, updatedAt: NOW,
};
const APP = { id: 'api', projectId: 'p1', name: 'api', image: 'ghcr.io/me/api:1.4', env: [], port: 8080, replicas: 1, tier: 'FREE', status: 'ACTIVE', version: 1, createdAt: NOW, updatedAt: NOW };

async function mockWorld(page: Page) {
  const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route(/^https?:\/\/[^/]+\/api\//, (route) => route.fulfill(json({ error: 'not mocked' }, 404)));
  await loginAs(page);
  const at = (pattern: string, body: unknown) => page.route(pattern, (route) => route.fulfill(json(body)));
  await at('**/api/config', { deploymentMode: 'cloud', appHosting: true, sites: true, features: { studio_v2: true } });
  await at('**/api/orgs', [{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' }]);
  await at('**/api/provision', [PROJECT]);
  await at('**/api/provision/p1', PROJECT);
  await at('**/api/tiers', [{ tier: 'FREE', cpu: '1', memory: '2Gi', storageSize: '10Gi' }]);
  await at('**/api/projects/p1/apps/', [APP]);
  await at('**/api/projects/p1/sites/', []);
  await at('**/api/projects/p1/storage/buckets', []);
  await at('**/api/projects/p1/functions', []);
  await at('**/api/provision/p1/credentials', {
    projectId: 'p1', host: INTERNAL_HOST, port: 5432, databaseName: 'app', username: 'excalibase_app', sslMode: 'require',
    connectionUrl: `postgresql://excalibase_app:x@${INTERNAL_HOST}:5432/app`,
  });
  await at('**/api/provision/p1/metrics/current', { cpuUsagePercent: 12, memoryUsagePercent: 30, diskUsageGB: 1.2, activeConnections: 4, maxConnections: 100 });
}

async function expectNoHorizontalScroll(page: Page) {
  const { scroll, client } = await page.evaluate(() => ({
    scroll: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    client: document.documentElement.clientWidth,
  }));
  expect(scroll).toBeLessThanOrEqual(client);
}

async function contentWidth(page: Page) {
  return (await page.locator('main').boundingBox())?.width ?? 0;
}

const views = [
  { name: 'project-home', url: '/project/p1' },
  { name: 'services-view', url: '/project/p1?service=app%3Aapi' },
  { name: 'database-panel', url: '/project/p1?service=database' },
];

test.describe('Studio v2 on a phone', () => {
  for (const view of views) {
    test(`${view.name} has no horizontal scroll and full-width content`, async ({ page }) => {
      await mockWorld(page);
      await page.goto(view.url);
      await expect(page.getByRole('main')).toBeVisible();
      await page.waitForTimeout(1500);
      if (SHOTS) await page.screenshot({ path: `${SHOTS}/${view.name}.png` });
      await expect(page.getByRole('button', { name: 'Menu', exact: true })).toBeVisible();
      await expect(page.getByTestId('icon-rail')).toHaveCount(0);
      await expectNoHorizontalScroll(page);
      expect(await contentWidth(page)).toBeGreaterThanOrEqual(390 - 1);
    });
  }

  test('the menu opens a drawer that closes on navigation and on Escape', async ({ page }) => {
    await mockWorld(page);
    await page.goto('/project/p1');
    const menu = page.getByRole('button', { name: 'Menu', exact: true });
    await expect(menu).toHaveAttribute('aria-expanded', 'false');

    await menu.click();
    const drawer = page.getByRole('dialog', { name: 'Project navigation' });
    await expect(drawer).toBeVisible();
    await expect(menu).toHaveAttribute('aria-expanded', 'true');
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/drawer-open.png` });

    await page.keyboard.press('Escape');
    await expect(drawer).toHaveCount(0);
    await expect(menu).toHaveAttribute('aria-expanded', 'false');
    await expect(menu).toBeFocused();

    await menu.click();
    await drawer.getByTestId('nav-sql').click();
    await expect(page).toHaveURL(/\/project\/p1\/sql/);
    await expect(drawer).toHaveCount(0);
    await expectNoHorizontalScroll(page);
  });

  test('a wide window keeps the rail and has no menu button', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await mockWorld(page);
    await page.goto('/project/p1');
    await expect(page.getByTestId('icon-rail')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Menu', exact: true })).toHaveCount(0);
  });
});
