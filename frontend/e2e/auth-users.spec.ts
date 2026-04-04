import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Auth Users Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/auth/users');
  });

  test('renders users table', async ({ page }) => {
    await expect(page.getByTestId('auth-users-page')).toBeVisible();
    await expect(page.getByTestId('user-row-1')).toBeVisible();
    await expect(page.getByTestId('user-row-2')).toBeVisible();
  });

  test('shows user details', async ({ page }) => {
    await expect(page.getByText('alice@test.com')).toBeVisible();
    await expect(page.getByText('bob@test.com')).toBeVisible();
  });

  test('shows enabled/disabled status', async ({ page }) => {
    await expect(page.getByText('Active')).toBeVisible();
    await expect(page.getByText('Disabled')).toBeVisible();
  });

  test('search filters users', async ({ page }) => {
    await page.getByTestId('user-search').fill('alice');
    await expect(page.getByText('alice@test.com')).toBeVisible();
    await expect(page.getByText('bob@test.com')).not.toBeVisible();
  });
});

test.describe('Auth Sessions Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/auth/sessions');
  });

  test('renders sessions page', async ({ page }) => {
    await expect(page.getByTestId('auth-sessions-page')).toBeVisible();
    await expect(page.getByText('alice@test.com')).toBeVisible();
  });

  test('shows active session count', async ({ page }) => {
    await expect(page.getByText('Active (1)')).toBeVisible();
  });
});
