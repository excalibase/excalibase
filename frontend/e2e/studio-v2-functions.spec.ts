import { test, expect, type Page } from '@playwright/test';
import { loginAs, SHOTS_DIR } from './helpers';

// EXC-854: edge functions as a service card with a side panel, behind studio_v2.

const NOW = '2026-10-01T00:00:00Z';
const PROJECT = {
  projectId: 'p1', projectName: 'p1', orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE',
  status: 'ACTIVE', currentStage: 'COMPLETED', port: 5432, databaseName: 'app', username: 'excalibase_app',
  backupEnabled: true, backupSchedule: '0 2 * * *', createdAt: NOW, updatedAt: NOW,
};
const fn = (id: string, name: string, active: boolean, version: number) => ({
  id, projectId: 'p1', name, files: [{ path: 'index.ts', content: 'export default () => new Response("hi")' }], active, version, createdAt: NOW, updatedAt: NOW,
});
const FUNCTIONS = [fn('hello', 'Hello function', true, 3), fn('cleanup', 'Nightly cleanup', false, 1)];

async function mockWorld(page: Page, role = 'owner') {
  const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
  // The catch-all goes first, so the specific routes below (and loginAs's) win over it.
  await page.route(/^https?:\/\/[^/]+\/api\//, (route) => route.fulfill(json({ error: 'not mocked' }, 404)));
  await loginAs(page);
  const at = (pattern: string, body: unknown) => page.route(pattern, (route) => route.fulfill(json(body)));
  await at('**/api/config', { deploymentMode: 'cloud', appHosting: true, sites: true, apiUrl: 'https://api.example.com', features: { studio_v2: true } });
  await at('**/api/orgs', [{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role }]);
  await at('**/api/provision', [PROJECT]);
  await at('**/api/provision/p1', PROJECT);
  await at('**/api/tiers', [{ tier: 'FREE', cpu: '1', memory: '2Gi', storageSize: '10Gi' }]);
  await at('**/api/projects/p1/apps/', []);
  await at('**/api/projects/p1/sites/', []);
  await at('**/api/projects/p1/storage/buckets', []);
  await at('**/api/projects/p1/functions', FUNCTIONS);
  await at('**/api/projects/p1/functions/runtime/status', { status: 'healthy', healthy: true });
  await at('**/api/projects/p1/functions/secrets', [{ key: 'STRIPE_KEY' }]);
  await at('**/api/projects/p1/functions/hello/logs', { logs: [{ level: 'info', msg: 'hello started', ts: 1760000000000 }] });
  await at('**/api/provision/p1/metrics/current', { cpuUsagePercent: 12, memoryUsagePercent: 30, diskUsageGB: 1.2, activeConnections: 4, maxConnections: 100 });
}

const cards = (page: Page) => page.getByRole('list', { name: 'Services' });

test.describe('Studio v2 functions', () => {
  test('a Functions card shows the count and the runtime state', async ({ page }) => {
    await mockWorld(page);
    await page.goto('/project/p1');
    const card = cards(page).getByRole('link', { name: /Functions/ });
    await expect(card).toContainText('2 edge functions');
    await expect(card).toContainText('Running');
  });

  test('the panel lists functions with status and the public invoke URL', async ({ page }) => {
    await mockWorld(page);
    await page.goto('/project/p1');
    await cards(page).getByRole('link', { name: /Functions/ }).click();
    await expect(page.getByRole('tablist', { name: 'Functions' }).getByRole('tab')).toHaveText(['Functions', 'Logs', 'Secrets']);
    const hello = page.getByRole('listitem', { name: 'Hello function' });
    await expect(hello).toContainText('v3');
    await expect(hello).toContainText('Active');
    await expect(hello).toContainText('https://api.example.com/functions/v1/p1/hello');
    await expect(page.getByRole('listitem', { name: 'Nightly cleanup' })).toContainText('Inactive');
    await page.screenshot({ path: `${SHOTS_DIR}/exc854-functions-panel.png`, fullPage: true });
  });

  test('logs and secrets tabs read the runtime', async ({ page }) => {
    await mockWorld(page);
    await page.goto('/project/p1?service=functions&tab=logs');
    await expect(page.getByText('hello started')).toBeVisible();
    await page.getByRole('tab', { name: 'Secrets' }).click();
    await expect(page).toHaveURL(/tab=secrets/);
    await expect(page.getByText('STRIPE_KEY')).toBeVisible();
  });

  test('deleting a function needs its id typed, then calls the server', async ({ page }) => {
    await mockWorld(page);
    const deleted: string[] = [];
    await page.route('**/api/projects/p1/functions/hello', (route) => {
      if (route.request().method() === 'DELETE') {
        deleted.push(route.request().url());
        return route.fulfill({ status: 204 });
      }
      return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
    });
    await page.goto('/project/p1?service=functions');
    await page.getByRole('listitem', { name: 'Hello function' }).getByRole('button', { name: 'Delete' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('button', { name: 'Delete' })).toBeDisabled();
    expect(deleted).toHaveLength(0);
    await dialog.getByRole('textbox').fill('hello');
    await dialog.getByRole('button', { name: 'Delete' }).click();
    await expect.poll(() => deleted.length).toBe(1);
  });

  test('a viewer reads the list but gets no invoke or delete', async ({ page }) => {
    await mockWorld(page, 'viewer');
    await page.goto('/project/p1?service=functions');
    const hello = page.getByRole('listitem', { name: 'Hello function' });
    await expect(hello).toContainText('https://api.example.com/functions/v1/p1/hello');
    await expect(hello.getByRole('button', { name: 'Invoke', exact: true })).toHaveCount(0);
    await expect(hello.getByRole('button', { name: 'Delete', exact: true })).toHaveCount(0);
  });

  test('a project with no functions shows no Functions card', async ({ page }) => {
    await mockWorld(page);
    await page.route('**/api/projects/p1/functions', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }));
    await page.goto('/project/p1');
    await expect(page.getByTestId('services-empty').or(cards(page))).toBeVisible();
    await expect(page.getByRole('link', { name: /Functions/ })).toHaveCount(0);
  });
});
