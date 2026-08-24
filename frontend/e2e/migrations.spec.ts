import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Migrations Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/operations/migrations');
  });

  test('renders migrations page with history', async ({ page }) => {
    await expect(page.getByText('Migration History')).toBeVisible();
    await expect(page.getByText('create_users')).toBeVisible();
    await expect(page.getByText('V1')).toBeVisible();
  });
});
