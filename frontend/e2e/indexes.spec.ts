import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Indexes Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/indexes');
  });

  test('renders indexes page', async ({ page }) => {
    await expect(page.getByTestId('indexes-page')).toBeVisible();
  });

  test('selecting a table shows its indexes', async ({ page }) => {
    await expect(page.getByTestId('indexes-page')).toBeVisible();
    // Select users table from the dropdown
    await page.getByTestId('table-selector').selectOption('users');
    await expect(page.getByText('users_pkey')).toBeVisible();
    await expect(page.getByText('users_email_key')).toBeVisible();
  });

  test('create index button opens side panel', async ({ page }) => {
    await page.getByTestId('create-index-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
  });
});
