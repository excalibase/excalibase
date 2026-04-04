import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Edge Functions Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/edge-functions');
  });

  test('renders function list', async ({ page }) => {
    await expect(page.getByTestId('edge-functions-page')).toBeVisible();
    await expect(page.getByTestId('fn-item-hello')).toBeVisible();
  });

  test('shows runtime status', async ({ page }) => {
    await expect(page.getByText('Runtime healthy')).toBeVisible();
  });

  test('selecting function shows code', async ({ page }) => {
    await page.getByTestId('fn-item-hello').click();
    await expect(page.getByTestId('fn-code')).toBeVisible();
  });

  test('deploy button opens side panel', async ({ page }) => {
    await page.getByTestId('create-fn-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await expect(page.getByTestId('fn-id-input')).toBeVisible();
    await expect(page.getByTestId('fn-code-input')).toBeVisible();
  });

  test('delete shows confirm modal', async ({ page }) => {
    await page.getByTestId('fn-item-hello').click();
    await page.getByTestId('delete-fn-btn').click();
    await expect(page.getByTestId('confirm-modal')).toBeVisible();
  });
});
