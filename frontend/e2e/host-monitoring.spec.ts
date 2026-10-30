import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { loginAs, mockInstances, mockProject } from './helpers';

const ADMIN = { id: '1', username: 'admin', email: 'admin@test.com', role: 'platform_admin' };
const GIB = 1024 ** 3;

// Host monitoring ships dark (EXC-670); /api/config says whether it is on.
async function mockHostMetricsFeature(page: Page, hostmetrics: boolean) {
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'selfhosted', features: { hostmetrics } }),
    }),
  );
}

const sample = (timestamp: string, cpuPercent: number | null) => ({
  timestamp,
  cpuPercent,
  cpuCount: 4,
  load1: 0.5,
  load5: 0.4,
  load15: 0.3,
  memoryTotalBytes: 16 * GIB,
  memoryUsedBytes: 4 * GIB,
  swapTotalBytes: 0,
  swapUsedBytes: 0,
  diskTotalBytes: 100 * GIB,
  diskUsedBytes: 25 * GIB,
  diskReadBytesPerSec: 1024,
  diskWriteBytesPerSec: 2048,
  networkRxBytesPerSec: 4096,
  networkTxBytesPerSec: 8192,
  networkSource: 'containers',
  temperatureMaxCelsius: null,
  temperatures: [],
});

const container = (name: string, projectId: string, health: string) => ({
  name,
  projectId,
  component: projectId ? 'database' : 'platform',
  state: 'running',
  health,
  timestamp: '2026-10-29T10:00:00Z',
  cpuCores: 0.5,
  cpuPercent: 12.5,
  memoryBytes: 512 * 1024 ** 2,
  memoryLimitBytes: projectId ? null : GIB,
  networkRxBytesPerSec: 100,
  networkTxBytesPerSec: 200,
});

const settings = {
  enabled: true,
  cpuPercent: 90,
  memoryPercent: null,
  diskPercent: null,
  temperatureCelsius: null,
  load5: null,
  networkMbps: null,
  windowMinutes: 5,
  downMinutes: 3,
  emails: ['ops@example.test'],
  webhookUrl: 'https://hooks.example.test/…abcd',
  webhookSet: true,
};

interface Recorded {
  readonly requests: string[];
  readonly bodies: unknown[];
}

async function mockMonitoring(page: Page): Promise<Recorded> {
  const recorded: Recorded = { requests: [], bodies: [] };
  await page.route('**/api/admin/monitoring/**', (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace('/api/admin/monitoring', '');
    recorded.requests.push(`${request.method()} ${path}${url.search}`);
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (path === '/host') {
      return json({
        range: url.searchParams.get('range'),
        bucketSeconds: 60,
        current: sample('2026-10-29T10:01:00Z', 37.5),
        points: [sample('2026-10-29T10:00:00Z', null), sample('2026-10-29T10:01:00Z', 37.5)],
      });
    }
    if (path === '/containers') {
      return json({
        timestamp: '2026-10-29T10:00:00Z',
        containers: [
          container('excalibase-provisioning', '', 'healthy'),
          container('excalibase-db-shop', 'shop', 'unhealthy'),
        ],
      });
    }
    if (path.startsWith('/containers/')) {
      return json({
        name: 'excalibase-db-shop',
        range: '1h',
        bucketSeconds: 60,
        points: [container('excalibase-db-shop', 'shop', '')],
      });
    }
    if (path === '/alert-settings/test') {
      return json({
        results: [
          { channel: 'email', ok: true },
          { channel: 'webhook', ok: false, error: 'timed out' },
        ],
      });
    }
    if (path === '/alert-settings' && request.method() === 'PUT') {
      const body = request.postDataJSON() as Record<string, unknown>;
      recorded.bodies.push(body);
      if (body.webhookUrl === 'http://plain')
        return json({ error: 'webhookUrl must be an https URL' }, 400);
      return json({
        ...settings,
        ...body,
        webhookUrl: body.webhookUrl === '' ? null : settings.webhookUrl,
        webhookSet: body.webhookUrl !== '',
      });
    }
    return json(settings);
  });
  return recorded;
}

test.describe('Host monitoring', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page, ADMIN);
    await mockInstances(page);
  });

  test('the admin opens Host from the navigation and sees the host and its containers', async ({
    page,
  }) => {
    await mockHostMetricsFeature(page, true);
    const recorded = await mockMonitoring(page);
    await page.goto('/');
    await page.getByTestId('platform-nav').getByRole('link', { name: 'Host', exact: true }).click();
    await expect(page).toHaveURL(/\/admin\/host$/);

    const cards = page.getByTestId('host-cards');
    await expect(cards.getByText('37.5%')).toBeVisible();
    await expect(cards.getByText('4.0 GB / 16.0 GB')).toBeVisible();
    await expect(
      page.getByText("host interfaces not readable; showing the containers' sum"),
    ).toBeVisible();
    await expect(page.getByTestId('host-charts')).toBeVisible();

    const db = page.getByTestId('container-excalibase-db-shop');
    await expect(db.getByText('running · unhealthy')).toBeVisible();
    await expect(db.getByText('512.0 MB / no limit')).toBeVisible();
    await expect(db.getByRole('link', { name: 'shop' })).toHaveAttribute('href', '/project/shop');

    await page.getByRole('button', { name: '24h' }).click();
    await expect.poll(() => recorded.requests).toContain('GET /host?range=24h');
    await db.getByRole('button', { name: 'History of excalibase-db-shop' }).click();
    await expect(page.getByTestId('history-excalibase-db-shop')).toBeVisible();
    await expect
      .poll(() => recorded.requests)
      .toContain('GET /containers/excalibase-db-shop?range=24h');
  });

  test('alert settings save, clear the webhook, show refusals and test each channel', async ({
    page,
  }) => {
    await mockHostMetricsFeature(page, true);
    const recorded = await mockMonitoring(page);
    await page.goto('/admin/host');
    await expect(page.getByRole('textbox', { name: 'CPU %' })).toHaveValue('90');

    await page.getByRole('textbox', { name: 'New webhook URL' }).fill('http://plain');
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByRole('alert')).toHaveText('webhookUrl must be an https URL');

    await page.getByRole('textbox', { name: 'New webhook URL' }).fill('');
    await page.getByRole('textbox', { name: 'Disk %' }).fill('80');
    await page.getByRole('button', { name: 'Clear webhook' }).click();
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByText('Saved.')).toBeVisible();
    expect(recorded.bodies.at(-1)).toMatchObject({
      diskPercent: 80,
      webhookUrl: '',
      emails: ['ops@example.test'],
    });
    await expect(page.getByTestId('webhook-state')).toHaveText('No webhook set');

    await page.getByRole('button', { name: 'Send test' }).click();
    await expect(page.getByTestId('test-results')).toContainText('Email: sent');
    await expect(page.getByTestId('test-results')).toContainText('Webhook: failed, timed out');
  });

  test('with the feature off Host is not offered and the page calls nothing', async ({ page }) => {
    await mockHostMetricsFeature(page, false);
    const recorded = await mockMonitoring(page);
    await page.goto('/');
    await expect(
      page.getByTestId('platform-nav').getByRole('link', { name: 'Platform Admin' }),
    ).toBeVisible();
    await expect(
      page.getByTestId('platform-nav').getByRole('link', { name: 'Host', exact: true }),
    ).toHaveCount(0);
    await page.goto('/admin/host');
    await expect(page.getByTestId('feature-unavailable')).toBeVisible();
    expect(recorded.requests).toHaveLength(0);
  });
});

test.describe('Project metrics containers', () => {
  test('the project metrics page lists its containers when host metrics are on', async ({
    page,
  }) => {
    await loginAs(page);
    await mockProject(page);
    await mockHostMetricsFeature(page, true);
    await page.route('**/api/provision/test-project/metrics/**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: 'null' }),
    );
    const requests: string[] = [];
    await page.route('**/api/projects/test-project/containers/metrics*', (route) => {
      requests.push(new URL(route.request().url()).search);
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          range: '1h',
          bucketSeconds: 60,
          containers: [
            {
              name: 'excalibase-db-test-project',
              component: 'database',
              state: 'running',
              health: 'healthy',
              current: container('excalibase-db-test-project', 'test-project', 'healthy'),
              points: [container('excalibase-db-test-project', 'test-project', 'healthy')],
            },
          ],
        }),
      });
    });
    await page.goto('/project/test-project/monitoring/metrics');
    const section = page.getByTestId('project-containers');
    await expect(section.getByText('excalibase-db-test-project')).toBeVisible();
    await expect(section.getByText('running · healthy')).toBeVisible();
    await section.getByRole('button', { name: '7d' }).click();
    await expect.poll(() => requests).toContain('?range=7d');
  });
});
