import { test, expect, Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

// useEdgeFunctions hooks fetch functions list, runtime status, secrets,
// and per-function logs. Without mocks, the page renders an empty state
// instead of the seeded `hello` function the original test assumed.
async function mockEdgeFunctionsAPIs(page: Page) {
  // EdgeFunctionsPage reads fn.files.length on each row, so the mock
  // must include a files array.
  const helloFn = {
    id: 'hello',
    name: 'Hello function',
    version: 1,
    files: [{ path: 'index.ts', content: 'export default async () => new Response("hi");' }],
    createdAt: '2026-01-01T00:00:00Z',
  };
  await page.route('**/api/projects/test-project/functions', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([helloFn]),
      });
    }
    return route.continue();
  });
  await page.route('**/api/projects/test-project/functions/runtime/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'healthy', healthy: true }),
    }),
  );
  await page.route('**/api/projects/test-project/functions/hello', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(helloFn) }),
  );
  await page.route('**/api/projects/test-project/functions/hello/logs', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ logs: [] }),
    }),
  );
  await page.route('**/api/projects/test-project/functions/secrets', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }),
  );
}

test.describe('Edge Functions Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockEdgeFunctionsAPIs(page);
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
