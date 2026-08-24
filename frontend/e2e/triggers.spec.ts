import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Triggers Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/triggers');
  });

  test('renders triggers page with empty state', async ({ page }) => {
    await expect(page.getByTestId('triggers-page')).toBeVisible();
    await expect(page.getByText('No triggers found')).toBeVisible();
  });

  test('create trigger button opens side panel', async ({ page }) => {
    await expect(page.getByTestId('triggers-page')).toBeVisible();
    await page.getByTestId('create-trigger-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
  });
});
