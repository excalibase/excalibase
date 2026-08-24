import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('Functions Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('renders empty state', async ({ page }) => {
    await page.goto('/project/test-project/database/functions');
    await expect(page.getByTestId('functions-page')).toBeVisible();
    await expect(page.getByText('No functions defined yet')).toBeVisible();
  });

  test('renders functions when present', async ({ page }) => {
    await page.route('**/api/schema/test-project/functions*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { name: 'get_user', schema: 'public', language: 'plpgsql', returnType: 'text', argTypes: 'integer', volatility: 'STABLE', definition: 'BEGIN RETURN \'hello\'; END;' },
        ]),
      })
    );
    await page.goto('/project/test-project/database/functions');
    await expect(page.getByTestId('fn-get_user')).toBeVisible();
    await expect(page.getByText('plpgsql')).toBeVisible();
    await expect(page.getByText('STABLE')).toBeVisible();
  });

  test('create function button opens side panel', async ({ page }) => {
    await page.goto('/project/test-project/database/functions');
    await page.getByTestId('create-function-btn').click();
    await expect(page.getByTestId('sidepanel')).toBeVisible();
    await expect(page.getByTestId('fn-name-input')).toBeVisible();
    await expect(page.getByTestId('fn-body-input')).toBeVisible();
  });

  test('expand function shows definition', async ({ page }) => {
    await page.route('**/api/schema/test-project/functions*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { name: 'my_func', schema: 'public', language: 'sql', returnType: 'integer', argTypes: '', volatility: 'IMMUTABLE', definition: 'SELECT 42' },
        ]),
      })
    );
    await page.goto('/project/test-project/database/functions');
    await page.getByTestId('fn-my_func').click();
    await expect(page.getByText('SELECT 42')).toBeVisible();
  });
});
