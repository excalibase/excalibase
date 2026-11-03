import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { loginAs, mockProject } from './helpers';

// Slow queries ships dark (EXC-654); /api/config says whether it is on.
async function mockSlowQueriesFeature(page: Page, slowqueries: boolean) {
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'cloud', features: { slowqueries } }),
    }),
  );
}

async function mockReport(page: Page, report: Record<string, unknown>) {
  const requests: string[] = [];
  await page.route('**/api/provision/test-project/slow-queries/**', (route) => {
    requests.push(`${route.request().method()} ${new URL(route.request().url()).pathname}${new URL(route.request().url()).search}`);
    if (route.request().method() === 'POST') return route.fulfill({ status: 204 });
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(report) });
  });
  return requests;
}

const listed = {
  engine: 'postgresql', enabled: true, sort: 'total', canReset: true, canEnable: true,
  queries: [{ query: 'SELECT * FROM orders WHERE id = $1', calls: 42, totalTimeMs: 1250, meanTimeMs: 29.8, rows: 42 }],
};

test.describe('Slow queries', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
  });

  test('lists the statements, re-sorts by mean and resets after confirming', async ({ page }) => {
    await mockSlowQueriesFeature(page, true);
    const requests = await mockReport(page, listed);
    await page.goto('/project/test-project/monitoring/slow-queries');
    await expect(page.getByTestId('slow-queries-page')).toBeVisible();
    await expect(page.getByText('SELECT * FROM orders WHERE id = $1')).toBeVisible();
    await expect(page.getByText('1.25 s')).toBeVisible();

    await page.getByRole('button', { name: 'Mean time' }).click();
    await expect.poll(() => requests).toContain('GET /api/provision/test-project/slow-queries/?sort=mean&limit=50');

    await page.getByRole('button', { name: 'Reset statistics' }).click();
    await page.getByRole('button', { name: 'Reset', exact: true }).click();
    await expect.poll(() => requests).toContain('POST /api/provision/test-project/slow-queries/reset');
  });

  test('an older database explains the restart before turning statistics on', async ({ page }) => {
    await mockSlowQueriesFeature(page, true);
    const requests = await mockReport(page, { ...listed, enabled: false, queries: [] });
    await page.goto('/project/test-project/monitoring/slow-queries');
    await expect(page.getByText('Query statistics are off for this database')).toBeVisible();
    await page.getByRole('button', { name: 'Turn on query statistics' }).click();
    await page.getByRole('button', { name: 'Turn on', exact: true }).click();
    await expect.poll(() => requests).toContain('POST /api/provision/test-project/slow-queries/enable');
  });

  test('with the feature off the page is not offered and calls nothing', async ({ page }) => {
    await mockSlowQueriesFeature(page, false);
    const requests = await mockReport(page, listed);
    await page.goto('/project/test-project/monitoring/slow-queries');
    await expect(page.getByTestId('feature-unavailable')).toBeVisible();
    expect(requests).toHaveLength(0);
  });
});
