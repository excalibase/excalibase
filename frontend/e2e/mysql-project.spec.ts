import { test, expect, Page } from '@playwright/test';
import { loginAs, mockSchemaEndpoints } from './helpers';

// ADR 0041: MySQL projects ship dark behind the mysql feature.
const FIRST_RENDER = 20_000;
const PROJECT_ID = 'shop-mysql';
const HOST = 'moco-shop-mysql-mysql-primary.org-1-shop-mysql.svc.cluster.local';

async function json(page: Page, pattern: string, body: unknown) {
  await page.route(pattern, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

async function mockPlatform(page: Page, mysql: boolean) {
  await json(page, '**/api/config', { deploymentMode: 'cloud', features: { mysql } });
  await json(page, '**/api/postgres/catalog', { majors: [{ major: '17', available: true, documentDb: true }] });
  await json(page, '**/api/mysql/catalog', { defaultVersion: '8.4.10', versions: [{ version: '8.4.10' }, { version: '8.4.9' }] });
  await json(page, '**/api/orgs', [{ id: 'org-1', name: 'Org One', slug: 'org-one', tier: 'FREE', ownerId: '1' }]);
}

test.describe('MySQL projects', () => {
  test('the MySQL card is closed while the feature is off', async ({ page }) => {
    await loginAs(page);
    await mockPlatform(page, false);
    await page.goto('/provision');
    await expect(page.getByTestId('engine-MYSQL')).toBeDisabled({ timeout: FIRST_RENDER });
  });

  test('creates a MySQL project on a catalogue version', async ({ page }) => {
    await loginAs(page);
    await mockPlatform(page, true);
    let requested: unknown;
    await page.route('**/api/provision', (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      requested = route.request().postDataJSON();
      return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ projectId: PROJECT_ID }) });
    });
    await page.goto('/provision');
    await page.getByTestId('engine-MYSQL').click({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('mysql-version-8.4.10')).toHaveAttribute('aria-pressed', 'true');
    await page.getByTestId('mysql-version-8.4.9').click();
    await page.getByLabel('Project Name').fill('shop');
    await page.getByTestId('provision-submit').click();
    await expect.poll(() => requested).toEqual({ projectName: 'shop', orgId: 'org-1', databaseType: 'MYSQL', mysqlVersion: '8.4.9' });
  });

  test('a MySQL project lists only what works on it', async ({ page }) => {
    await loginAs(page);
    await mockPlatform(page, true);
    await mockSchemaEndpoints(page, PROJECT_ID);
    await json(page, `**/api/provision/${PROJECT_ID}`, {
      projectId: PROJECT_ID, orgId: 'org-1', databaseType: 'MYSQL', mysqlVersion: '8.4.10', tier: 'FREE',
      namespace: 'org-1-shop-mysql', status: 'ACTIVE', currentStage: 'COMPLETED', host: HOST, port: 3306,
      databaseName: 'app', createdAt: '2026-10-11T00:00:00Z', updatedAt: '2026-10-11T00:00:00Z',
    });
    await page.goto(`/project/${PROJECT_ID}`);
    await expect(page.getByTestId('service-database-mysql-endpoint')).toContainText(`${HOST}:3306`, { timeout: FIRST_RENDER });
    await expect(page.getByTestId('nav-settings')).toBeVisible();
    await expect(page.getByTestId('nav-sql')).toHaveCount(0);
    await expect(page.getByTestId('nav-database')).toHaveCount(0);
    await page.goto(`/project/${PROJECT_ID}/sql`);
    await expect(page.getByTestId('postgres-required')).toBeVisible({ timeout: FIRST_RENDER });
  });
});
