import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

// The API page shows the public addresses a developer calls and a snippet that
// runs as copied; never the database's in-cluster host or a guessed port.
test.describe('API Info Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.route('**/api/config', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ deploymentMode: 'cloud', apiUrl: 'https://api.example.test' }),
      }),
    );
    await page.goto('/project/test-project/api');
  });

  test('renders API info page', async ({ page }) => {
    await expect(page.getByTestId('api-info-page')).toBeVisible();
  });

  test('shows the public GraphQL and REST endpoints', async ({ page }) => {
    await expect(page.getByText('https://api.example.test/test-project/graphql')).toBeVisible();
    await expect(page.getByText('https://api.example.test/test-project/api/v1')).toBeVisible();
  });

  test('shows an SDK snippet built for this project', async ({ page }) => {
    await expect(page.getByTestId('connect-code')).toContainText("from '@excalibase/sdk'");
    await expect(page.getByTestId('connect-code')).toContainText("projectId: 'test-project'");
  });

  test('never shows an in-cluster address', async ({ page }) => {
    await expect(page.getByTestId('api-info-page')).toBeVisible();
    await expect(page.getByText(/svc\.cluster\.local|:3000|:4000/)).toHaveCount(0);
  });
});
