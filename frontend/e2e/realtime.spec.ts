import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Realtime Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/realtime');
  });

  test('renders realtime page', async ({ page }) => {
    await expect(page.getByTestId('realtime-page')).toBeVisible();
  });

  test('shows CDC architecture info', async ({ page }) => {
    await expect(page.getByText('CDC via excalibase-watcher')).toBeVisible();
    await expect(page.getByText('PostgreSQL Logical Replication (WAL)')).toBeVisible();
  });

  test('shows event types', async ({ page }) => {
    await expect(page.getByText('INSERT')).toBeVisible();
    await expect(page.getByText('UPDATE')).toBeVisible();
    await expect(page.getByText('DELETE')).toBeVisible();
  });

  test('shows coming soon for live inspector', async ({ page }) => {
    await expect(page.getByText('Live Inspector coming soon')).toBeVisible();
  });
});
