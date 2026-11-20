import { test, expect, type Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

// EXC-666: the read-only address card on Settings, against a mocked control plane.

const endpoint = {
  projectId: 'test-project', publicOffered: true, publicEnabled: true, available: true,
  host: 'test-project.db.example.test', port: 31001, requireTls: true, database: 'testdb', username: 'app',
  connectionStrings: { requireTls: '', allowPlaintext: '' }, caCertificate: '',
  canChange: true,
};

const readAddress = {
  projectId: 'test-project', replicas: 2,
  publicOffered: true, publicOpen: true, enabled: false, available: false,
  host: 'test-project.db.example.test', canChange: true,
};

async function mockControlPlane(page: Page, features: Record<string, boolean>, view = readAddress) {
  await page.route('**/api/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'cloud', features }) }),
  );
  await page.route('**/api/projects/test-project/db-endpoint', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(endpoint) }),
  );
  const puts: unknown[] = [];
  await page.route('**/api/projects/test-project/db-read-address', (route) => {
    if (route.request().method() === 'PUT') {
      puts.push(route.request().postDataJSON());
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ ...view, enabled: true, available: true, port: 31002 }),
      });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(view) });
  });
  return puts;
}

test.describe('Read-only address', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('hidden while the flag is off', async ({ page }) => {
    await mockControlPlane(page, {});
    await page.goto('/project/test-project/settings');
    await expect(page.getByTestId('public-port-card')).toBeVisible();
    await expect(page.getByTestId('read-address-card')).toHaveCount(0);
  });

  test('an admin opens the read port beside the public port', async ({ page }) => {
    const puts = await mockControlPlane(page, { dbreadaddress: true });
    await page.goto('/project/test-project/settings');
    const card = page.getByTestId('read-address-card');
    await expect(card.getByTestId('read-address-replicas')).toHaveText('2 replicas serve reads.');
    await expect(card.getByTestId('read-address-internal')).toHaveCount(0);
    await expect(page.locator('body')).not.toContainText('svc.cluster.local');
    await card.getByTestId('read-address-toggle').click();
    await expect(card.getByTestId('read-address-public')).toContainText('test-project.db.example.test:31002');
    expect(puts).toEqual([{ enabled: true }]);
  });

  // EXC-675: the card names the read-only login; no password is stored or shown (EXC-759).
  test('the read address signs in with the read-only login', async ({ page }) => {
    await mockControlPlane(page, { dbreadaddress: true, dbreadrole: true },
      { ...readAddress, username: 'excalibase_reader', readOnlyLogin: true } as never);
    await page.route('**/api/provision/test-project/credentials', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          projectId: 'test-project', host: 'localhost', port: 5432, databaseName: 'testdb', username: 'app',
          connectionUrl: 'postgresql://app:<password>@localhost:5432/testdb',
          readerUsername: 'excalibase_reader',
        }),
      }),
    );
    await page.goto('/project/test-project/settings');
    await expect(page.getByTestId('read-address-login')).toContainText('excalibase_reader, a login that can only read');
    await expect(page.getByTestId('conn-reader-username').first()).toHaveText('excalibase_reader');
    await expect(page.getByTestId('conn-reader-password')).toHaveCount(0);
    await expect(page.getByTestId('conn-reveal-password')).toHaveCount(0);
    await expect(page.locator('body')).not.toContainText('reader-pass');
  });

  test('one copy explains there is no replica', async ({ page }) => {
    await mockControlPlane(page, { dbreadaddress: true }, { ...readAddress, replicas: 0, reason: 'one-copy', internal: undefined } as never);
    await page.goto('/project/test-project/settings');
    await expect(page.getByTestId('read-address-none')).toContainText('one copy');
    await expect(page.getByTestId('read-address-toggle')).toHaveCount(0);
  });
});
