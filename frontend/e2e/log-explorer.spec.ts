import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Log Explorer Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/monitoring/logs');
  });

  test('renders log explorer page', async ({ page }) => {
    await expect(page.getByTestId('log-explorer-page')).toBeVisible();
  });

  test('shows search input', async ({ page }) => {
    await expect(page.getByTestId('log-search-input')).toBeVisible();
  });

  test('displays log lines', async ({ page }) => {
    await expect(page.getByText('Starting database')).toBeVisible();
  });
});
