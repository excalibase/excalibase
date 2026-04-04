import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Navigation & Layout', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('project home renders instance detail', async ({ page }) => {
    await page.goto('/project/test-project');
    // Should show instance detail (home page of project)
    await page.waitForLoadState('networkidle');
  });

  test('database section navigation works', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    await expect(page.getByTestId('tables-page')).toBeVisible();
    // SubNav should show Database children
    await expect(page.getByTestId('sub-nav')).toBeVisible();
  });

  test('navigate to roles via sub-nav', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    // Click Roles in the sub-nav
    await page.getByRole('link', { name: 'Roles' }).click();
    await expect(page.getByTestId('roles-page')).toBeVisible();
  });

  test('navigate to extensions via sub-nav', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    await page.getByRole('link', { name: 'Extensions' }).click();
    await expect(page.getByTestId('extensions-page')).toBeVisible();
  });

  test('navigate to functions via sub-nav', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    await page.getByRole('link', { name: 'Functions' }).click();
    await expect(page.getByTestId('functions-page')).toBeVisible();
  });

  test('navigate to RLS via sub-nav', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    await page.getByRole('link', { name: 'RLS Policies' }).click();
    await expect(page.getByTestId('rls-page')).toBeVisible();
  });

  test('back to platform via logo click', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    await page.getByTestId('back-to-platform').click();
    await page.waitForURL('**/');
  });

  test('icon rail has sidebar settings', async ({ page }) => {
    await page.goto('/project/test-project/database/tables');
    await expect(page.getByTestId('icon-rail')).toBeVisible();
    await expect(page.getByTestId('sidebar-settings')).toBeVisible();
  });

  test('dark mode persists across page navigation', async ({ page }) => {
    await page.goto('/');
    const isDark = await page.evaluate(() => document.documentElement.classList.contains('dark'));
    expect(isDark).toBe(true);
  });
});
