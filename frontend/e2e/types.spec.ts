import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Types Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/types');
  });

  test('renders types page', async ({ page }) => {
    await expect(page.getByTestId('types-page')).toBeVisible();
  });

  test('renders enum type with values as badges', async ({ page }) => {
    await expect(page.getByText('status_enum')).toBeVisible();
    await expect(page.getByText('active', { exact: true })).toBeVisible();
    await expect(page.getByText('inactive', { exact: true })).toBeVisible();
  });
});
