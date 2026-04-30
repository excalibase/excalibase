import { test, expect, Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

interface MockState {
  tables: Array<{ schema: string; table: string; enabled: boolean }>;
}

async function mockRealtimeAPIs(page: Page, initial: MockState) {
  const state: MockState = JSON.parse(JSON.stringify(initial));

  await page.route('**/api/projects/*/realtime/tables', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(state.tables),
      });
    }
    return route.continue();
  });

  await page.route('**/api/projects/*/realtime/tables/*/*', (route) => {
    const url = new URL(route.request().url());
    const m = url.pathname.match(/realtime\/tables\/([^/]+)\/([^/]+)$/);
    if (!m) return route.continue();
    const [, schema, table] = m;
    const target = state.tables.find((t) => t.schema === schema && t.table === table);
    if (!target) return route.fulfill({ status: 404, body: '{}' });

    if (route.request().method() === 'PUT') {
      target.enabled = true;
    } else if (route.request().method() === 'DELETE') {
      target.enabled = false;
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(target),
    });
  });

  await page.route('**/api/projects/*/realtime/enable-all', (route) => {
    let added = 0;
    state.tables.forEach((t) => {
      if (!t.enabled) {
        t.enabled = true;
        added++;
      }
    });
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ added }),
    });
  });

  await page.route('**/api/projects/*/realtime/disable-all', (route) => {
    let dropped = 0;
    state.tables.forEach((t) => {
      if (t.enabled) {
        t.enabled = false;
        dropped++;
      }
    });
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ dropped }),
    });
  });

  return state;
}

test.describe('Realtime page', () => {
  test.beforeEach(async ({ page }) => {
    // loginAs now stubs vault-guard endpoints automatically (see helpers.ts)
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('renders all tables with initial disabled state', async ({ page }) => {
    await mockRealtimeAPIs(page, {
      tables: [
        { schema: 'public', table: 'posts', enabled: false },
        { schema: 'public', table: 'comments', enabled: false },
        { schema: 'nosql', table: 'notes', enabled: false },
      ],
    });

    await page.goto('/project/test-project/realtime');
    await expect(page.getByTestId('realtime-page')).toBeVisible();
    await expect(page.getByTestId('realtime-counter')).toContainText('0');
    await expect(page.getByTestId('realtime-counter')).toContainText('3');

    for (const id of ['public-posts', 'public-comments', 'nosql-notes']) {
      const cb = page.getByTestId(`realtime-toggle-${id}`);
      await expect(cb).not.toBeChecked();
    }
  });

  test('per-row toggle calls API and updates state', async ({ page }) => {
    await mockRealtimeAPIs(page, {
      tables: [{ schema: 'public', table: 'posts', enabled: false }],
    });

    await page.goto('/project/test-project/realtime');
    const toggle = page.getByTestId('realtime-toggle-public-posts');
    await expect(toggle).not.toBeChecked();

    await toggle.click();
    await expect(toggle).toBeChecked();

    await toggle.click();
    await expect(toggle).not.toBeChecked();
  });

  test('enable-all action requires confirmation then enables all', async ({ page }) => {
    await mockRealtimeAPIs(page, {
      tables: [
        { schema: 'public', table: 'posts', enabled: false },
        { schema: 'public', table: 'comments', enabled: false },
      ],
    });

    await page.goto('/project/test-project/realtime');
    await page.getByTestId('realtime-enable-all').click();
    await expect(page.getByText('Enable realtime for all tables?')).toBeVisible();
    await page.getByRole('button', { name: 'Enable all' }).last().click();

    await expect(page.getByTestId('realtime-toggle-public-posts')).toBeChecked();
    await expect(page.getByTestId('realtime-toggle-public-comments')).toBeChecked();
  });

  test('disable-all action requires confirmation then disables all', async ({ page }) => {
    await mockRealtimeAPIs(page, {
      tables: [
        { schema: 'public', table: 'posts', enabled: true },
        { schema: 'public', table: 'comments', enabled: true },
      ],
    });

    await page.goto('/project/test-project/realtime');
    await page.getByTestId('realtime-disable-all').click();
    await expect(page.getByText('Disable realtime for all tables?')).toBeVisible();
    await page.getByRole('button', { name: 'Disable all' }).last().click();

    await expect(page.getByTestId('realtime-toggle-public-posts')).not.toBeChecked();
  });

  test('search filters the table list', async ({ page }) => {
    await mockRealtimeAPIs(page, {
      tables: [
        { schema: 'public', table: 'posts', enabled: false },
        { schema: 'public', table: 'comments', enabled: false },
        { schema: 'nosql', table: 'notes', enabled: true },
      ],
    });

    await page.goto('/project/test-project/realtime');
    await page.getByTestId('realtime-search').fill('notes');

    await expect(page.getByTestId('realtime-row-nosql-notes')).toBeVisible();
    await expect(page.getByTestId('realtime-row-public-posts')).not.toBeVisible();
  });

  test('show only enabled filter hides disabled tables', async ({ page }) => {
    await mockRealtimeAPIs(page, {
      tables: [
        { schema: 'public', table: 'posts', enabled: true },
        { schema: 'public', table: 'comments', enabled: false },
      ],
    });

    await page.goto('/project/test-project/realtime');
    await page.getByTestId('realtime-filter-enabled').check();

    await expect(page.getByTestId('realtime-row-public-posts')).toBeVisible();
    await expect(page.getByTestId('realtime-row-public-comments')).not.toBeVisible();
  });
});
