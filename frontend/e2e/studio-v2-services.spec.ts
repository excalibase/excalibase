import { test, expect, type Page } from '@playwright/test';
import { loginAs, FORCED_V2 } from './helpers';

// EXC-762 step 3: the project's Services tab with a side panel per service, behind studio_v2.

const INTERNAL_HOST = 'p1-postgres-rw.org-1.svc.cluster.local';

const ORGS = [{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' }];

function project(projectId: string, extra: object = {}) {
  return {
    projectId, projectName: projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE',
    status: 'ACTIVE', currentStage: 'COMPLETED', port: 5432, databaseName: 'app', username: 'excalibase_app',
    backupEnabled: true, backupSchedule: '0 2 * * *', createdAt: '2026-10-01T00:00:00Z', updatedAt: '2026-10-01T00:00:00Z', ...extra,
  };
}

const APP = { id: 'api', projectId: 'p1', name: 'api', image: 'ghcr.io/me/api:1.4', env: [], port: 8080, replicas: 1, tier: 'FREE', status: 'ACTIVE', version: 1, createdAt: '2026-10-02T00:00:00Z', updatedAt: '2026-10-02T00:00:00Z' };
const SITE = { id: 'web', projectId: 'p1', name: 'web', hostname: 'web.excalibase.app', url: 'https://web.excalibase.app', spaFallback: true, liveDeployId: 'sd1', createdBy: '1', createdAt: '2026-10-02T00:00:00Z', updatedAt: '2026-10-02T00:00:00Z' };
const deploy = (id: string, revision: number, status: string) => ({ id, appId: 'api', projectId: 'p1', revision, image: 'ghcr.io/me/api:1.4', status, createdBy: '1', createdAt: `2026-10-0${revision}T00:00:00Z` });

interface World {
  projects: Array<ReturnType<typeof project>>;
  apps?: unknown[];
  sites?: unknown[];
  buckets?: unknown[];
  functions?: unknown[];
  appDeploys?: unknown[];
  siteDeploys?: unknown[];
}

async function mockWorld(page: Page, world: World) {
  const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
  // The catch-all goes first, so the specific routes below (and loginAs's) win over it.
  // Only the server's /api: the dev server also serves /src/api/*.ts modules.
  await page.route(/^https?:\/\/[^/]+\/api\//, (route) => route.fulfill(json({ error: 'not mocked' }, 404)));
  await loginAs(page);
  const at = (pattern: string, body: unknown, status = 200) => page.route(pattern, (route) => route.fulfill(json(body, status)));
  await at('**/api/config', { deploymentMode: 'cloud', appHosting: true, sites: true, features: { studio_v2: true, dballowlist: true, onetimecredentials: true } });
  await at('**/api/orgs', ORGS);
  await at('**/api/provision', world.projects);
  for (const p of world.projects) await at(`**/api/provision/${p.projectId}`, p);
  await at('**/api/tiers', [{ tier: 'FREE', cpu: '1', memory: '2Gi', storageSize: '10Gi' }]);
  await at('**/api/projects/p1/apps/', world.apps ?? []);
  await at('**/api/projects/p1/apps/api', APP);
  await at('**/api/projects/p1/apps/api/deploys*', world.appDeploys ?? []);
  await at('**/api/projects/p1/sites/', world.sites ?? []);
  await at('**/api/projects/p1/sites/web', SITE);
  await at('**/api/projects/p1/sites/web/deploys', world.siteDeploys ?? []);
  await at('**/api/projects/p1/storage/buckets', world.buckets ?? []);
  await at('**/api/projects/p1/functions', world.functions ?? []);
  await at('**/api/provision/p1/credentials', {
    projectId: 'p1', host: INTERNAL_HOST, port: 5432, databaseName: 'app', username: 'excalibase_app', sslMode: 'require',
    connectionUrl: `postgresql://excalibase_app:x@${INTERNAL_HOST}:5432/app`,
  });
  await at('**/api/projects/p1/db-endpoint', {
    projectId: 'p1', publicOffered: true, publicEnabled: false, available: false, host: '', port: 0, requireTls: true,
    database: 'app', username: 'excalibase_app', connectionStrings: { requireTls: '', allowPlaintext: '' }, caCertificate: '',
    internal: { host: INTERNAL_HOST, port: 5432 }, canChange: true,
  });
  await at('**/api/projects/p1/db-endpoint/allowed-sources', { projectId: 'p1', access: 'unset', sources: [], max: 20, publicEnabled: false, canChange: true, callerAddress: '198.51.100.23' });
  await at('**/api/provision/p1/metrics/current', { cpuUsagePercent: 12, memoryUsagePercent: 30, diskUsageGB: 1.2, activeConnections: 4, maxConnections: 100 });
  await at('**/api/provision/p1/backup/list', { backups: [{ id: 'b1', timestamp: '2026-10-03T02:00:00Z', status: 'COMPLETED', sizeBytes: 1024 }] });
}

const FULL: World = {
  projects: [project('p1')],
  apps: [APP],
  sites: [SITE],
  buckets: [{ id: 'bk1', projectId: 'p1', name: 'avatars', public: false, createdAt: '2026-10-02T00:00:00Z', updatedAt: '2026-10-02T00:00:00Z' }],
  appDeploys: [deploy('d2', 2, 'succeeded'), deploy('d1', 1, 'superseded')],
  siteDeploys: [
    { id: 'sd2', siteId: 'web', projectId: 'p1', status: 'FAILED', fileCount: 1, totalBytes: 10, spaFallback: true, createdBy: '1', createdAt: '2026-10-05T00:00:00Z', live: false },
    { id: 'sd1', siteId: 'web', projectId: 'p1', status: 'SUCCEEDED', fileCount: 1, totalBytes: 10, spaFallback: true, createdBy: '1', createdAt: '2026-10-04T00:00:00Z', live: true },
  ],
};

const cards = (page: Page) => page.getByRole('list', { name: 'Services' });

test.describe('Studio v2 services', () => {
  test('lists the database, app, site and bucket with live status', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.goto('/project/p1');
    await expect(cards(page).getByRole('link')).toHaveCount(5);
    const database = cards(page).getByRole('link', { name: /Database/ });
    await expect(database).toContainText('Postgres 17 · 1 vCPU');
    await expect(database).toContainText('Running');
    await expect(cards(page).getByRole('link', { name: /api/ })).toContainText('ghcr.io/me/api:1.4');
    await expect(cards(page).getByRole('link', { name: /api/ })).toContainText('Running');
    await expect(cards(page).getByRole('link', { name: /web/ })).toContainText('Last deploy failed · live on previous');
    await expect(cards(page).getByRole('link', { name: /avatars/ })).toContainText('Storage bucket · private');
    await expect(page.getByRole('button', { name: '+ Add service' })).toBeVisible();
  });

  test('selecting a card opens its panel with the right tabs', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.goto('/project/p1');
    await cards(page).getByRole('link', { name: /api/ }).click();
    await expect(page).toHaveURL(/service=app%3Aapi|service=app:api/);
    const tabs = page.getByRole('tablist', { name: 'App' });
    await expect(tabs.getByRole('tab')).toHaveText(['Overview', 'Deploys', 'Variables', 'Metrics', 'Logs', 'Settings']);
    await expect(cards(page).getByRole('link', { name: /api/ })).toHaveAttribute('aria-current', 'true');

    await cards(page).getByRole('link', { name: /Database/ }).click();
    await expect(page.getByRole('tablist', { name: 'Database' }).getByRole('tab')).toHaveText(['Overview', 'Connect', 'Data', 'Backups', 'Metrics', 'Logs', 'Settings']);

    await cards(page).getByRole('link', { name: /web/ }).click();
    await expect(page.getByRole('tablist', { name: 'Site' }).getByRole('tab')).toHaveText(['Overview', 'Deploys', 'Variables', 'Settings']);

    await page.getByRole('button', { name: 'Close panel' }).click();
    await expect(page.getByRole('tablist')).toHaveCount(0);
  });

  test('the back button steps back through selections', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.goto('/project/p1');
    await cards(page).getByRole('link', { name: /api/ }).click();
    await page.getByRole('tab', { name: 'Logs' }).click();
    await expect(page).toHaveURL(/tab=logs/);
    await page.goBack();
    await expect(page.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true');
    await page.goBack();
    await expect(page.getByRole('tablist')).toHaveCount(0);
  });

  test('a deep link opens the service and the tab', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.goto('/project/p1?service=database&tab=backups');
    await expect(page.getByRole('tab', { name: 'Backups' })).toHaveAttribute('aria-selected', 'true');
    await expect(cards(page).getByRole('link', { name: /Database/ })).toHaveAttribute('aria-current', 'true');
  });

  test('Connect shows no internal host and no stored password', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.goto('/project/p1?service=database&tab=connect');
    await expect(page.getByRole('tab', { name: 'Connect' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByTestId('connection-strings')).toBeVisible();
    await expect(page.getByTestId('public-port-card')).toBeVisible();
    await expect(page.getByTestId('allow-list')).toBeVisible();
    const body = page.locator('body');
    await expect(body).not.toContainText('svc.cluster.local');
    await expect(body).not.toContainText(INTERNAL_HOST);
    await expect(page.getByTestId('conn-password')).toHaveCount(0);
  });

  test('Database overview: live state, a pause card that needs an admin, and the delete flow', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.goto('/project/p1?service=database');
    await expect(page.getByRole('heading', { name: 'Live state' })).toBeVisible();
    await expect(page.getByText('4 of 100')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Pause this database' })).toBeVisible();
    await expect(page.getByText('What goes down')).toBeVisible();
    await expect(page.getByText('What stays')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Pause database' })).toBeEnabled();
    await page.getByTestId('delete-project-btn').click();
    await expect(page.getByRole('dialog')).toContainText('p1');
  });

  test('a viewer cannot pause and is told to ask an admin', async ({ page }) => {
    await mockWorld(page, FULL);
    await page.route('**/api/orgs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ ...ORGS[0], role: 'viewer' }]) }));
    await page.goto('/project/p1?service=database');
    await expect(page.getByRole('button', { name: 'Pause database' })).toBeDisabled();
    await expect(page.getByText(/ask an admin/i)).toBeVisible();
  });

  test('an empty project offers to add a service', async ({ page }) => {
    await mockWorld(page, { projects: [project('p1', { noDatabase: true, canAddDatabase: true })] });
    await page.goto('/project/p1');
    await expect(page.getByTestId('services-empty')).toBeVisible();
    await expect(page.getByRole('heading', { name: /add your first service/i })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Services' })).toHaveCount(0);
  });

  test('a project with only an app never shows database pages', async ({ page }) => {
    await mockWorld(page, { projects: [project('p1', { noDatabase: true })], apps: [APP], appDeploys: [deploy('d1', 1, 'succeeded')] });
    await page.goto('/project/p1');
    await expect(cards(page).getByRole('link')).toHaveCount(2);
    await expect(page.getByRole('link', { name: /Database/ })).toHaveCount(0);
    await cards(page).getByRole('link', { name: /api/ }).click();
    await expect(page.getByRole('tab', { name: 'Connect' })).toHaveCount(0);
    await expect(page.getByTestId('database-required')).toHaveCount(0);
    // A link that still names the database lands on the app, not a dead end.
    await page.goto('/project/p1?service=database&tab=data');
    await expect(page.getByRole('tab', { name: 'Data' })).toHaveCount(0);
    await expect(page.getByTestId('database-required')).toHaveCount(0);
    await expect(cards(page).getByRole('link', { name: /api/ })).toBeVisible();
  });

  test('flag off keeps the old overview', async ({ page }) => {
    test.skip(FORCED_V2, 'asserts the flag-off layout; the run forces studio_v2 on');
    await mockWorld(page, FULL);
    await page.route('**/api/config', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'cloud', appHosting: true, features: { studio_v2: false } }) }));
    await page.goto('/project/p1');
    await expect(page.getByTestId('project-overview')).toBeVisible();
    await expect(page.getByRole('list', { name: 'Services' })).toHaveCount(0);
  });

  test('phone width stacks the panel under the cards', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await mockWorld(page, FULL);
    await page.goto('/project/p1?service=database');
    const list = await cards(page).boundingBox();
    const panel = await page.getByRole('tablist', { name: 'Database' }).boundingBox();
    expect(list && panel && panel.y > list.y + list.height - 1).toBeTruthy();
  });
});
