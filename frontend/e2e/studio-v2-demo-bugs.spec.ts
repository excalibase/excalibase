import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-870: bugs found while recording the Studio v2 demo (behind studio_v2).

const SHOTS = process.env.EXC870_SHOTS;
const NOW = '2026-10-01T00:00:00Z';
const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

function project(projectId: string, extra: object = {}) {
  return {
    projectId, projectName: `Project ${projectId}`, orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE',
    status: 'ACTIVE', currentStage: 'COMPLETED', port: 5432, databaseName: 'app', username: 'excalibase_app', createdAt: NOW, updatedAt: NOW, ...extra,
  };
}

async function mockWorld(page: Page, config: object = {}) {
  await page.route(/^https?:\/\/[^/]+\/api\//, (route) => route.fulfill(json({ error: 'not mocked' }, 404)));
  await loginAs(page);
  const at = (pattern: string, body: unknown) => page.route(pattern, (route) => route.fulfill(json(body)));
  await at('**/api/config', { deploymentMode: 'selfhosted', appHosting: true, sites: false, features: { studio_v2: true }, ...config });
  await at('**/api/orgs', [{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' }]);
  await at('**/api/provision', [project('p1'), project('long-named-project-two', { projectName: 'A long named demo project' })]);
  await at('**/api/provision/p1', project('p1'));
  await at('**/api/tiers', [{ tier: 'FREE', cpu: '1', memory: '2Gi', storageSize: '10Gi' }]);
  await at('**/api/projects/*/apps/', []);
  await at('**/api/projects/*/sites/', []);
}

test.describe('Studio v2 demo bugs', () => {
  test('the projects table fits a phone', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await mockWorld(page);
    await page.goto('/instances');
    await expect(page.getByRole('link', { name: /Project p1/ }).first()).toBeVisible();
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/projects.png` });
    const table = page.getByRole('table', { name: 'Projects' });
    const { scroll, client } = await table.evaluate((el) => ({ scroll: el.parentElement!.scrollWidth, client: el.parentElement!.clientWidth }));
    expect(scroll).toBeLessThanOrEqual(client);
    // Every column of a row is on screen: nothing is reached by scrolling sideways.
    for (const cell of [table.getByText('Running').first(), table.getByRole('link', { name: /^Open/ }).first()]) {
      const box = await cell.boundingBox();
      expect(box!.x + box!.width).toBeLessThanOrEqual(390);
    }
  });

  test('opening the Connect tab does not scroll the page by itself', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 600 });
    await mockWorld(page, { apiUrl: 'https://api.example.com', features: { studio_v2: true, dballowlist: true } });
    await page.route('**/api/provision/p1/credentials', (route) => route.fulfill(json({
      projectId: 'p1', host: 'db.example.com', port: 5432, databaseName: 'app', username: 'excalibase_app', sslMode: 'require', connectionUrl: 'postgresql://x',
    })));
    await page.route('**/api/schema/p1/tables*', (route) => route.fulfill(json([{ name: 'todos' }])));
    await page.route('**/api/projects/p1/db-endpoint', (route) => route.fulfill(json({ host: 'db.example.com', port: 5432 })));
    await page.goto('/project/p1?service=database&tab=connect');
    await expect(page.getByTestId('connect-snippets')).toBeVisible();
    await expect(page.getByTestId('connection-strings')).toBeVisible();
    await page.waitForTimeout(500);
    expect(await page.locator('main').evaluate((main) => main.scrollTop)).toBe(0);
  });

  test('the phone drawer uses the v2 names', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await mockWorld(page);
    await page.goto('/project/p1/database/overview');
    await page.getByRole('button', { name: 'Menu', exact: true }).click();
    const drawer = page.getByRole('dialog', { name: 'Project navigation' });
    await expect(drawer.getByTestId('nav-containers')).toHaveText('Apps');
    await expect(drawer.getByTestId('nav-database')).toHaveText('Database');
    await expect(drawer.getByText(/^(Databases|Containers|Vault)$/)).toHaveCount(0);
  });

  test('app hosting and storage that are off are said so, never a red failure', async ({ page }) => {
    await mockWorld(page, { appHosting: false });
    await page.goto('/project/p1');
    await expect(page.getByTestId('not-offered')).toHaveText('App hosting and file storage are not offered on this install.');
    await expect(page.getByRole('alert')).toHaveCount(0);
    await page.goto('/project/p1/variables');
    await expect(page.getByTestId('variables-not-offered')).toBeVisible();
    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('a single host without monitoring says metrics and logs are not on, with no Kubernetes', async ({ page }) => {
    await mockWorld(page);
    await page.route('**/api/provision/p1/metrics/current', (route) => route.fulfill(json({
      metricsAvailable: false, unavailableReason: 'database metrics are not turned on for this single host: switch on host monitoring to see them',
    })));
    await page.route('**/api/provision/p1/logs*', (route) => route.fulfill(json({
      logs: '', notOffered: 'log search is not turned on for this single host: switch on host monitoring to search the database\'s logs',
    })));
    await page.goto('/project/p1?service=database&tab=metrics');
    await expect(page.getByText(/^Database metrics are not turned on for this single host/)).toBeVisible();
    await page.getByRole('tab', { name: 'Logs' }).click();
    await expect(page.getByTestId('logs-not-offered')).toBeVisible();
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expect(page.getByText(/kubernetes/i)).toHaveCount(0);
  });
});
