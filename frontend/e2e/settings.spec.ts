import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Settings Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/settings');
  });

  test('renders project settings', async ({ page }) => {
    await expect(page.getByTestId('settings-page')).toBeVisible();
    // The project ref appears in multiple connection/code blocks, so
    // the loose text query is ambiguous. Pin to the connect-section
    // block which is the page's primary identity surface.
    await expect(
      page.getByTestId('connect-section').getByText('test-project', { exact: true }),
    ).toBeVisible();
    const settingsPage = page.getByTestId('settings-page');
    await expect(settingsPage.getByText('POSTGRESQL')).toBeVisible();
    await expect(settingsPage.getByText('FREE')).toBeVisible();
  });

  test('shows danger zone with delete button', async ({ page }) => {
    await expect(page.getByText('Danger Zone')).toBeVisible();
    await expect(page.getByTestId('delete-project-btn')).toBeVisible();
  });

  test('delete project shows confirm modal with type confirmation', async ({ page }) => {
    await page.getByTestId('delete-project-btn').click();
    await expect(page.getByTestId('confirm-modal')).toBeVisible();
    await expect(page.getByTestId('confirm-input')).toBeVisible();
    // Must type project name
    await expect(page.getByTestId('modal-confirm')).toBeDisabled();
    await page.getByTestId('confirm-input').fill('test-project');
    await expect(page.getByTestId('modal-confirm')).toBeEnabled();
  });
});
