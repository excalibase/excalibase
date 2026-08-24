import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Roles Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/roles');
  });

  test('renders roles table', async ({ page }) => {
    await expect(page.getByTestId('roles-page')).toBeVisible();
    await expect(page.getByTestId('role-row-postgres')).toBeVisible();
    await expect(page.getByTestId('role-row-excalibase_app')).toBeVisible();
  });

  test('superuser cannot be dropped', async ({ page }) => {
    const pgRow = page.getByTestId('role-row-postgres');
    await expect(pgRow.getByText('YES', { exact: true }).first()).toBeVisible(); // superuser badge
    // No drop button for superuser
    await expect(page.getByTestId('drop-role-postgres')).not.toBeVisible();
  });

  test('non-superuser has drop button', async ({ page }) => {
    await expect(page.getByTestId('drop-role-excalibase_app')).toBeVisible();
  });

  test('create role button opens side panel', async ({ page }) => {
    await page.getByTestId('create-role-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await expect(page.getByTestId('role-name-input')).toBeVisible();
    await expect(page.getByTestId('role-password-input')).toBeVisible();
  });

  test('drop role shows confirm modal', async ({ page }) => {
    await page.getByTestId('drop-role-excalibase_app').click();
    await expect(page.getByTestId('confirm-modal')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Drop Role' })).toBeVisible();
  });
});
