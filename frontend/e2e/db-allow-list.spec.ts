import { test, expect, Page } from '@playwright/test';
import { loginAs, mockInstances, mockSchemaEndpoints } from './helpers';

// EXC-665: who may reach the public database port, edited on the Settings page.
const PROJECT_ID = 'test-project';
const FIRST_RENDER = 20_000;
const SOURCES = `**/api/projects/${PROJECT_ID}/db-endpoint/allowed-sources`;

const INTERNAL = {
  host: 'test-project-postgres-rw.org-1.svc.cluster.local',
  port: 5432,
  connectionString: 'postgresql://excalibase_app@test-project-postgres-rw.org-1.svc.cluster.local:5432/app?sslmode=require',
};

async function json(page: Page, pattern: string, body: unknown) {
  await page.route(pattern, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

async function mockProject(page: Page, allowList: boolean) {
  await mockInstances(page, PROJECT_ID);
  await mockSchemaEndpoints(page, PROJECT_ID);
  await json(page, '**/api/config', { deploymentMode: 'cloud', features: { dballowlist: allowList } });
  await json(page, `**/api/provision/${PROJECT_ID}`, {
    projectId: PROJECT_ID, databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'org-1-test-project',
    status: 'ACTIVE', currentStage: 'COMPLETED', host: INTERNAL.host, port: 5432, databaseName: 'app',
    postgresVersion: '17', documentDb: false, backupEnabled: false, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
  });
  await json(page, `**/api/projects/${PROJECT_ID}/db-endpoint`, {
    projectId: PROJECT_ID, publicOffered: true, publicEnabled: false, available: false, host: '', port: 0,
    requireTls: true, database: 'app', username: 'excalibase_app',
    connectionStrings: { requireTls: '', allowPlaintext: '' }, caCertificate: '', internal: INTERNAL, canChange: true,
  });
}

test.describe('Database allow-list', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
  });

  test('an Admin chooses who may connect before the port can open', async ({ page }) => {
    await mockProject(page, true);
    let saved: unknown;
    await page.route(SOURCES, async (route) => {
      if (route.request().method() === 'PUT') {
        saved = route.request().postDataJSON();
        return route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ projectId: PROJECT_ID, access: 'listed', sources: ['198.51.100.23/32', '203.0.113.0/24'], max: 20, publicEnabled: false, canChange: true, callerAddress: '198.51.100.23' }),
        });
      }
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ projectId: PROJECT_ID, access: 'unset', sources: [], max: 20, publicEnabled: false, canChange: true, callerAddress: '198.51.100.23' }),
      });
    });
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('allow-list')).toBeVisible({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('public-port-toggle')).toBeDisabled();

    await page.getByRole('button', { name: 'Add my address (198.51.100.23)' }).click();
    await page.getByLabel('Address or range').fill('203.0.113.0/24');
    await page.getByRole('button', { name: 'Add', exact: true }).click();
    await page.getByTestId('allow-list-save').click();

    await expect(page.getByTestId('allow-list-state')).toContainText('Only the addresses below');
    expect(saved).toEqual({ sources: ['198.51.100.23/32', '203.0.113.0/24'] });
    await expect(page.getByTestId('public-port-toggle')).toBeEnabled();
  });

  test('is absent while the feature is off', async ({ page }) => {
    await mockProject(page, false);
    let asked = false;
    await page.route(SOURCES, (route) => {
      asked = true;
      return route.fulfill({ status: 404, body: '404 page not found\n' });
    });
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('public-port-toggle')).toBeEnabled({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('allow-list')).toHaveCount(0);
    expect(asked).toBe(false);
  });
});
