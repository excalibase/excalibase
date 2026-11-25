import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-857: the database panel's Backups, Metrics and Logs tabs, behind studio_v2.

const ORG_ROLE = { current: 'owner' };

const PROJECT = {
  projectId: 'p1', projectName: 'p1', orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE',
  status: 'ACTIVE', currentStage: 'COMPLETED', port: 5432, databaseName: 'app', username: 'excalibase_app',
  createdAt: '2026-10-01T00:00:00Z', updatedAt: '2026-10-01T00:00:00Z',
};

const METRICS = {
  metricsAvailable: true, unavailableReason: null, healthStatus: 'HEALTHY', cpuUsageCores: 0.25, cpuLimitCores: 2, instanceCount: 2,
  memoryUsageMB: 512, memoryLimitMB: 2048, storageLimit: '10Gi', databaseSizeBytes: 2048, activeConnections: 3, maxConnections: 50,
  lastBackupTime: '2026-10-02T03:00:00Z',
  pods: [{ name: 'db-1', role: 'primary', cpuCores: 0.2, cpuLimitCores: 1, memoryMB: 300, memoryLimitMB: 1024 }],
};

const BACKUPS = {
  backupEnabled: true, schedule: '0 2 * * *', retentionDays: 14,
  backups: [
    { id: 'backup-001', timestamp: '2026-10-01T02:00:00Z', type: 'SCHEDULED', status: 'COMPLETED', sizeBytes: 2048 },
    { id: 'backup-002', timestamp: '2026-10-03T02:00:00Z', type: 'MANUAL', status: 'FAILED' },
  ],
};

const LOGS = '2026-10-02 LOG: checkpoint complete\n2026-10-02 ERROR: relation "x" does not exist\n2026-10-02 WARNING: slow statement';

async function mockWorld(page: Page, requests: string[]) {
  const json = (body: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route(/^https?:\/\/[^/]+\/api\//, (route) => route.fulfill(json({ error: 'not mocked' }, 404)));
  await loginAs(page);
  const at = (pattern: string, body: unknown) => page.route(pattern, (route) => route.fulfill(json(body)));
  await at('**/api/config', { deploymentMode: 'cloud', features: { studio_v2: true } });
  await page.route('**/api/orgs', (route) => route.fulfill(json([{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: ORG_ROLE.current }])));
  await at('**/api/provision', [PROJECT]);
  await at('**/api/provision/p1', PROJECT);
  await at('**/api/tiers', [{ tier: 'FREE', cpu: '1', memory: '2Gi', storageSize: '10Gi' }]);
  for (const kind of ['apps', 'sites', 'functions', 'storage/buckets']) await at(`**/api/projects/p1/${kind}${kind.includes('/') ? '' : '/'}`, []);
  await at('**/api/provision/p1/metrics/current', METRICS);
  await at('**/api/provision/p1/metrics/history*', { metrics: null });
  await at('**/api/provision/p1/backup/list', BACKUPS);
  await page.route('**/api/provision/p1/backup/trigger', (route) => {
    requests.push('trigger');
    return route.fulfill(json({ id: 'backup-003', status: 'IN_PROGRESS' }));
  });
  await page.route('**/api/provision/p1/backup/restore', (route) => {
    requests.push(`restore ${route.request().postData()}`);
    return route.fulfill(json({ id: 'job-1', status: 'RUNNING', mode: 'new_project' }));
  });
  await page.route('**/api/provision/p1/backup/restore/job-1', (route) => route.fulfill(json({ id: 'job-1', status: 'RUNNING', mode: 'new_project' })));
  await page.route('**/api/provision/p1/logs*', (route) => {
    requests.push(new URL(route.request().url()).search);
    return route.fulfill(json({ logs: LOGS }));
  });
}

const open = async (page: Page, tab: string) => {
  await page.goto(`/project/p1?service=database&tab=${tab}`);
  await expect(page.getByRole('tab', { name: new RegExp(tab, 'i'), selected: true })).toBeVisible();
};

// The shell clips the page and <main> scrolls on its own, so the page-level check alone cannot
// fail: <main> and the tab's own root are measured too.
const fitsScreen = async (page: Page, tabRoot: string) => {
  const pageWidth = await page.evaluate(() => ({ scroll: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth), client: document.documentElement.clientWidth }));
  expect(pageWidth.scroll).toBeLessThanOrEqual(pageWidth.client);
  for (const container of [page.locator('main'), page.getByTestId(tabRoot)]) {
    const { scroll, client } = await container.evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
    expect(scroll).toBeLessThanOrEqual(client);
  }
};

for (const [name, size] of [['desktop', { width: 1280, height: 800 }], ['phone', { width: 390, height: 800 }]] as const) {
  test.describe(`Studio v2 database tabs, ${name}`, () => {
    let requests: string[];

    test.beforeEach(async ({ page }) => {
      ORG_ROLE.current = 'owner';
      requests = [];
      await page.setViewportSize(size);
      await mockWorld(page, requests);
    });

    test('backups: history, back up now, and a restore into a new project', async ({ page }) => {
      await open(page, 'backups');
      await expect(page.getByText('Nightly backups at 02:00 UTC')).toBeVisible();
      await expect(page.getByTestId('backup-row-backup-002')).toBeVisible();
      await page.getByRole('button', { name: 'Back up now' }).click();
      await expect(page.getByText('Backup started')).toBeVisible();
      expect(requests).toContain('trigger');

      const start = page.getByRole('button', { name: 'Restore Latest Backup' });
      await expect(start).toBeDisabled();
      await page.getByLabel(/New project name/).fill('orders restored');
      await start.click();
      await expect(page.getByTestId('restore-result')).toBeVisible();
      expect(requests.some((r) => r.startsWith('restore') && r.includes('orders restored'))).toBe(true);
      await fitsScreen(page, 'db-backups');
    });

    test('backups: a viewer sees no controls', async ({ page }) => {
      ORG_ROLE.current = 'viewer';
      await open(page, 'backups');
      await expect(page.getByText(/Only an admin of this organization can back up or restore/)).toBeVisible();
      await expect(page.getByRole('button', { name: /Back up now|Restore|Replace/ })).toHaveCount(0);
      await expect(page.getByTestId('backup-row-backup-001')).toHaveCount(0);
    });

    test('metrics: usage against limits, with no infrastructure words', async ({ page }) => {
      await open(page, 'metrics');
      await expect(page.getByText('0.25 cores')).toBeVisible();
      await expect(page.getByText('3 of 50')).toBeVisible();
      await expect(page.getByTestId('instance-db-1')).toContainText('primary');
      await expect(page.getByTestId('db-metrics')).not.toContainText(/pod|kubernetes|cluster/i);
      await fitsScreen(page, 'db-metrics');
    });

    test('logs: lines, a wider range and a search reach the server', async ({ page }) => {
      await open(page, 'logs');
      await expect(page.getByText(/relation "x" does not exist/)).toBeVisible();
      await page.getByRole('button', { name: '24h' }).click();
      await expect.poll(() => requests.at(-1) ?? '').toContain('since=24h');
      await page.getByRole('searchbox', { name: 'Search logs' }).fill('ERROR');
      await expect(page.getByText(/checkpoint complete/)).toHaveCount(0);
      await expect.poll(() => requests.at(-1) ?? '').toContain('q=ERROR');
      await fitsScreen(page, 'db-logs');
    });
  });
}
