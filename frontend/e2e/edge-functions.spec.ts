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

  test("a refused deploy shows the server's reason", async ({ page }) => {
    await page.route('**/api/projects/test-project/functions', (route) =>
      route.request().method() === 'POST'
        ? route.fulfill({
            status: 400, contentType: 'application/json',
            body: JSON.stringify({ error: 'invalid function id: must be lowercase letters, digits and hyphens', status: 400 }),
          })
        : route.fallback(),
    );
    await page.getByTestId('create-fn-btn').click();
    await page.getByTestId('fn-id-input').fill('Bad_Id');
    await page.locator('#fn-name-field').fill('Bad');
    await page.getByTestId('submit-fn-btn').click();
    await expect(page.getByText('invalid function id: must be lowercase letters, digits and hyphens')).toBeVisible();
    await expect(page.getByText(/status code/)).toHaveCount(0);
  });

  // EXC-555: a function that answers 401 signed the developer out of Studio.
  test("a function's 401 is shown as its answer and keeps the developer signed in", async ({ page }) => {
    await page.route('**/api/projects/test-project/functions/hello/invoke', (route) =>
      route.fulfill({
        status: 401,
        // As the server sends it: Studio runs on another origin here.
        headers: { 'X-Excalibase-Function-Response': '1', 'Access-Control-Expose-Headers': 'X-Excalibase-Function-Response' },
        body: 'nope',
      }),
    );
    await page.getByTestId('fn-item-hello').click();
    await page.getByTestId('invoke-btn').click();
    await expect(page.getByTestId('invoke-result')).toHaveText(/401[\s\S]*nope/);
    await expect(page).toHaveURL(/edge-functions/);
  });

  test('delete shows confirm modal', async ({ page }) => {
    await page.getByTestId('fn-item-hello').click();
    await page.getByTestId('delete-fn-btn').click();
    await expect(page.getByTestId('confirm-modal')).toBeVisible();
  });
});
