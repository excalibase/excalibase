import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Extensions Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/extensions');
  });

  test('renders installed and available sections', async ({ page }) => {
    await expect(page.getByTestId('extensions-page')).toBeVisible();
    await expect(page.getByText('Installed (1)')).toBeVisible();
    await expect(page.getByText('Available (2)')).toBeVisible();
  });

  test('shows installed extension with version', async ({ page }) => {
    await expect(page.getByTestId('ext-plpgsql')).toBeVisible();
    await expect(page.getByText('v1.0')).toBeVisible();
  });

  test('shows available extension with enable button', async ({ page }) => {
    await expect(page.getByTestId('ext-uuid-ossp')).toBeVisible();
    await expect(page.getByTestId('enable-ext-uuid-ossp')).toBeVisible();
  });

  test('search filters extensions', async ({ page }) => {
    await page.getByTestId('ext-search').fill('uuid');
    await expect(page.getByTestId('ext-uuid-ossp')).toBeVisible();
    await expect(page.getByTestId('ext-plpgsql')).not.toBeVisible();
    await expect(page.getByTestId('ext-pgcrypto')).not.toBeVisible();
  });

  test('disable button shows confirm modal', async ({ page }) => {
    await page.getByTestId('disable-ext-plpgsql').click();
    await expect(page.getByTestId('confirm-modal')).toBeVisible();
    await expect(page.getByText('Disable Extension')).toBeVisible();
  });
});
