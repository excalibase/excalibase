import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('API Info Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/api');
  });

  test('renders API info page', async ({ page }) => {
    await expect(page.getByTestId('api-info-page')).toBeVisible();
  });

  test('shows REST API endpoint', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'REST API' })).toBeVisible();
  });

  test('shows GraphQL API endpoint', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'GraphQL API' })).toBeVisible();
  });

  test('shows Auth API endpoint', async ({ page }) => {
    await expect(page.getByText('Auth API')).toBeVisible();
  });

  test('shows Database Direct endpoint', async ({ page }) => {
    await expect(page.getByText('Database Direct')).toBeVisible();
  });

  test('shows Edge Functions endpoint', async ({ page }) => {
    await expect(page.getByText('Edge Functions').first()).toBeVisible();
  });
});
