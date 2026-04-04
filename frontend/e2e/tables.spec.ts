import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Tables Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/tables');
  });

  test('renders table list', async ({ page }) => {
    await expect(page.getByTestId('tables-page')).toBeVisible();
    await expect(page.getByTestId('table-item-users')).toBeVisible();
    await expect(page.getByTestId('table-item-orders')).toBeVisible();
  });

  test('selecting a table shows columns in schema view', async ({ page }) => {
    await page.getByTestId('table-item-users').click();
    // Switch to Schema view
    await page.getByText('Schema').click();
    await expect(page.getByTestId('column-row-id')).toBeVisible();
    await expect(page.getByTestId('column-row-email')).toBeVisible();
    await expect(page.getByTestId('column-row-name')).toBeVisible();
  });

  test('shows PK and UQ badges in schema view', async ({ page }) => {
    await page.getByTestId('table-item-users').click();
    await page.getByText('Schema').click();
    const idRow = page.getByTestId('column-row-id');
    await expect(idRow.getByText('PK')).toBeVisible();
    const emailRow = page.getByTestId('column-row-email');
    await expect(emailRow.getByText('UQ')).toBeVisible();
  });

  test('new table button opens side panel', async ({ page }) => {
    await page.getByTestId('new-table-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await expect(page.getByTestId('table-name-input')).toBeVisible();
  });

  test('sidepanel closes on backdrop click', async ({ page }) => {
    await page.getByTestId('new-table-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await page.getByTestId('sidepanel-backdrop').click();
    await expect(page.getByTestId('sidepanel')).not.toBeVisible();
  });

  test('drop table shows confirm modal with type confirmation', async ({ page }) => {
    await page.getByTestId('table-item-users').click();
    await page.getByTestId('drop-table-btn').click();
    await expect(page.getByTestId('confirm-modal')).toBeVisible();
    await expect(page.getByTestId('confirm-input')).toBeVisible();
    await expect(page.getByTestId('modal-confirm')).toBeDisabled();
    await page.getByTestId('confirm-input').fill('users');
    await expect(page.getByTestId('modal-confirm')).toBeEnabled();
  });

  test('add column button opens side panel in schema view', async ({ page }) => {
    await page.getByTestId('table-item-users').click();
    await page.getByText('Schema').click();
    await page.getByTestId('add-column-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await expect(page.getByTestId('column-name-input')).toBeVisible();
  });

  test('data view shows insert row button', async ({ page }) => {
    await page.getByTestId('table-item-users').click();
    await expect(page.getByTestId('insert-row-btn')).toBeVisible();
  });

  test('export CSV button visible in data view', async ({ page }) => {
    await page.getByTestId('table-item-users').click();
    await expect(page.getByTestId('export-csv-btn')).toBeVisible();
  });
});
