import { test, expect } from '@playwright/test';
import { loginAs, mockCloudMode, mockInstances } from './helpers';

const mockOrgs = [
  { id: 'org-1', name: 'Test Org', slug: 'test-org', tier: 'FREE', ownerId: '1' },
];

async function mockProvisionEndpoints(page: import('@playwright/test').Page) {
  await page.route('**/api/auth/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: '1', username: 'admin', email: 'admin@test.com', role: 'platform_admin' }) })
  );
  await page.route('**/api/orgs', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockOrgs) });
    }
    return route.continue();
  });
  await page.route('**/api/provision/byoc', (route) => {
    if (route.request().method() === 'POST') {
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ projectId: 'my-byoc', status: 'ACTIVE', currentStage: 'COMPLETED' }),
      });
    }
    return route.continue();
  });
}

test.describe('BYOC Provisioning', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockCloudMode(page);
    await mockInstances(page);
    await mockProvisionEndpoints(page);
  });

  test('shows deployment mode selector with K8s, Docker, BYOC', async ({ page }) => {
    await page.goto('/provision');
    await expect(page.getByTestId('deploy-mode-selector')).toBeVisible();
    await expect(page.getByTestId('deploy-mode-k8s')).toBeVisible();
    await expect(page.getByTestId('deploy-mode-docker')).toBeVisible();
    await expect(page.getByTestId('deploy-mode-byoc')).toBeVisible();
  });

  test('selecting BYOC shows connection form, hides engine/tier', async ({ page }) => {
    await page.goto('/provision');
    await page.getByTestId('deploy-mode-byoc').click();
    // BYOC form visible
    await expect(page.getByTestId('byoc-form')).toBeVisible();
    // Engine and tier sections should NOT be visible
    await expect(page.getByText('Database Engine')).not.toBeVisible();
    await expect(page.getByText('Plan')).not.toBeVisible();
  });

  test('K8s mode shows engine and tier, hides BYOC form', async ({ page }) => {
    await page.goto('/provision');
    // K8s is default
    await expect(page.getByText('Database Engine')).toBeVisible();
    await expect(page.getByText('Plan')).toBeVisible();
    // BYOC form should NOT be visible
    await expect(page.getByTestId('byoc-form')).not.toBeVisible();
  });

  test('BYOC form has all connection fields', async ({ page }) => {
    await page.goto('/provision');
    await page.getByTestId('deploy-mode-byoc').click();
    await expect(page.getByPlaceholder('db.example.com')).toBeVisible();
    await expect(page.getByPlaceholder('mydb')).toBeVisible();
    await expect(page.getByPlaceholder('postgres')).toBeVisible();
  });

  test('submit button says Connect Database in BYOC mode', async ({ page }) => {
    await page.goto('/provision');
    await page.getByTestId('deploy-mode-byoc').click();
    await expect(page.getByText('Connect Database')).toBeVisible();
  });
});
