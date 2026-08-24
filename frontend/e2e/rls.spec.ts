import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('RLS Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/database/rls');
  });

  test('renders RLS page with empty state', async ({ page }) => {
    await expect(page.getByTestId('rls-page')).toBeVisible();
    await expect(page.getByText('No policies defined yet')).toBeVisible();
  });

  test('create policy button opens side panel', async ({ page }) => {
    await page.getByTestId('create-policy-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await expect(page.getByTestId('policy-name-input')).toBeVisible();
  });

  test('shows RLS toggle per table', async ({ page }) => {
    await expect(page.getByTestId('rls-toggle-users')).toBeVisible();
    await expect(page.getByTestId('rls-toggle-orders')).toBeVisible();
  });

  test('renders policies grouped by table when present', async ({ page }) => {
    // Override with policies
    await page.route('**/api/schema/test-project/policies*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { name: 'user_select', table: 'users', command: 'SELECT', roles: 'authenticated', using: 'auth.uid() = id', withCheck: null, permissive: true },
        ]),
      })
    );
    await page.goto('/project/test-project/database/rls');
    await expect(page.getByTestId('policy-row-user_select')).toBeVisible();
  });
});
