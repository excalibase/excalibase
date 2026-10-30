import { test, expect, type Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

const fit = {
  plan: 'STANDARD', appsNeeded: 1, appsHeld: 0, appsAllowed: 5, diskBytes: 1 << 30, diskCapBytes: 5 << 30,
  appCpu: '1', appMemory: '1Gi', privateNetworkOn: true, canTurnOnPrivateNetwork: true, databaseReady: true, refusals: [],
};

const starter = {
  id: 'redis', format: 'excalibase.template/v1', name: 'Redis', summary: 'Redis as an internal service.',
  category: 'starter', license: 'Redis 7.4 RSALv2 or SSPLv1', catalog: false,
  needsPrivateNetwork: true, needsDatabase: false, fit,
  apps: [{ name: 'redis', image: 'redis@sha256:858f', internal: true, internalPorts: [6379], replicas: 1, env: [] }],
};

const mongodb = {
  id: 'mongodb', format: 'excalibase.template/v1', name: 'MongoDB', summary: 'A MongoDB server.',
  category: 'database', license: 'SSPL-1.0 (MongoDB Community Server)', catalog: true,
  needsPrivateNetwork: true, needsDatabase: false, fit,
  apps: [{
    name: 'mongodb', image: 'mongo@sha256:b992', internal: true, internalPorts: [27017], replicas: 1, minMemory: '1Gi',
    disk: { mountPath: '/data/db', size: '1Gi' },
    env: [
      { name: 'MONGO_INITDB_ROOT_USERNAME', source: 'literal', value: 'root' },
      { name: 'MONGO_INITDB_ROOT_PASSWORD', source: 'generated' },
      { name: 'MONGODB_URL', source: 'app', showOnce: true },
    ],
  }],
};

// The catalog ships dark (EXC-669); the server lists it only with the flag on.
async function mockTemplates(page: Page, templates: unknown[]) {
  const deploys: unknown[] = [];
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'cloud', appHosting: true, features: { templatecatalog: true } }),
    }),
  );
  await page.route(/\/api\/projects\/test-project\/app-templates\/$/, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(templates) }),
  );
  await page.route(/\/api\/projects\/test-project\/app-templates\/mongodb\/deploy$/, (route) => {
    deploys.push(route.request().postDataJSON());
    return route.fulfill({
      status: 202, contentType: 'application/json',
      body: JSON.stringify({
        templateId: 'mongodb', privateNetworkTurnedOn: false,
        apps: [{ id: 'a1', name: 'mongodb', deployId: 'd1', deployStatus: 'rolling' }],
        credentials: [{ app: 'mongodb', name: 'MONGODB_URL', value: 'mongodb://root:generated@mongodb:27017/?authSource=admin' }],
      }),
    });
  });
  return deploys;
}

test.describe('Template catalog', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('lists the services you run apart from the starters, with their licence', async ({ page }) => {
    await mockTemplates(page, [starter, mongodb]);
    await page.goto('/project/test-project/containers/templates');
    const catalog = page.getByTestId('templates-catalog');
    const card = catalog.getByTestId('template-mongodb');
    await expect(card.getByTestId('template-category')).toHaveText('Database');
    await expect(card.getByTestId('template-license')).toContainText('SSPL-1.0');
    await expect(card.getByTestId('template-self-managed')).toContainText('You run this service');
    await expect(page.getByTestId('templates-starters').getByTestId('template-redis')).toBeVisible();
  });

  test('deploys a service and shows its connection URL once', async ({ page }) => {
    const deploys = await mockTemplates(page, [mongodb]);
    await page.goto('/project/test-project/containers/templates');
    await page.getByTestId('template-deploy-mongodb').click();
    await page.getByTestId('template-confirm-mongodb').click();
    const shown = page.getByTestId('template-credentials');
    await expect(shown).toContainText('mongodb://root:generated@mongodb:27017/?authSource=admin');
    await expect(shown).toContainText('cannot show them again');
    expect(deploys).toEqual([{ confirmPrivateNetwork: false }]);
  });
});
