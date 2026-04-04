import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

test.describe('SQL Editor Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await page.goto('/project/test-project/sql');
  });

  test('renders SQL editor with run button', async ({ page }) => {
    await expect(page.getByTestId('sql-editor-page')).toBeVisible();
    await expect(page.getByTestId('sql-editor')).toBeVisible();
    await expect(page.getByTestId('run-query-btn')).toBeVisible();
  });

  test('executes query and shows results', async ({ page }) => {
    await page.getByTestId('run-query-btn').click();
    await expect(page.getByTestId('query-results')).toBeVisible();
    // Should show the mocked result (1 row)
    await expect(page.getByText('1 row')).toBeVisible();
  });

  test('shows query history button', async ({ page }) => {
    await expect(page.getByTestId('history-btn')).toBeVisible();
  });

  test('shows error on failed query', async ({ page }) => {
    await page.route('**/api/schema/test-project/query', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'relation "nonexistent" does not exist' }),
      })
    );
    await page.getByTestId('run-query-btn').click();
    await expect(page.getByTestId('query-error')).toBeVisible();
    await expect(page.getByText('relation "nonexistent" does not exist')).toBeVisible();
  });
});
