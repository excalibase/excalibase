import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Advisors Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/advisors');
  });

  test('renders advisors page', async ({ page }) => {
    await expect(page.getByTestId('advisors-page')).toBeVisible();
  });

  test('shows performance tab with findings', async ({ page }) => {
    await expect(page.getByTestId('advisors-page')).toBeVisible();
    // Performance tab should be active by default
    await expect(page.getByText('Unindexed Foreign Key')).toBeVisible();
    await expect(page.getByText('orders', { exact: true })).toBeVisible();
  });

  test('shows security tab with findings', async ({ page }) => {
    await expect(page.getByTestId('advisors-page')).toBeVisible();
    await page.getByTestId('tab-security').click();
    await expect(page.getByText('RLS Disabled')).toBeVisible();
    await expect(page.getByText('users')).toBeVisible();
  });
});
