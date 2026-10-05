import { test, expect, type APIRequestContext, type Page } from '@playwright/test';

// Runs against a real Studio (the local AIO), never against mocks:
//   STUDIO_LIVE_URL=http://studio.local STUDIO_LIVE_USER=admin STUDIO_LIVE_PASSWORD=... \
//   STUDIO_LIVE_PROJECT_ID=<running project> npx playwright test e2e/live/permissions.live.spec.ts
// STUDIO_LIVE_URL must be the Studio origin the control plane trusts
// (its STUDIO_URL): cookie-authenticated writes from any other page are
// refused. STUDIO_LIVE_API_URL overrides the API origin (default: <STUDIO_LIVE_URL>/api).
// It provisions nothing: it uses the given project, creates one table and one
// function in it, and removes both (and their permissions) whatever happens.

const studioUrl = process.env.STUDIO_LIVE_URL;
const username = process.env.STUDIO_LIVE_USER;
const password = process.env.STUDIO_LIVE_PASSWORD;
const projectId = process.env.STUDIO_LIVE_PROJECT_ID;
const apiUrl = process.env.STUDIO_LIVE_API_URL ?? (studioUrl ? `${studioUrl.replace(/\/$/, '')}/api` : '');
// The spec's own API calls ride the page's session cookie, so they name the
// Studio page as their origin, as the browser does.
const asStudio = { headers: { Origin: studioUrl ? new URL(studioUrl).origin : '' } };

const suffix = Date.now().toString(36);
const table = `perm_live_${suffix}`;
const fn = `perm_live_fn_${suffix}`;

interface PermissionDocument {
  tables: Array<{ table: string; role: string; select?: unknown; insert?: unknown }>;
  functions: Array<{ function: string; exposedAs: string }>;
}

test.describe('API permissions against a live Studio', () => {
  test.skip(
    !studioUrl || !username || !password || !projectId,
    'set STUDIO_LIVE_URL, STUDIO_LIVE_USER, STUDIO_LIVE_PASSWORD and STUDIO_LIVE_PROJECT_ID to run',
  );
  test.setTimeout(180_000);

  async function signIn(page: Page) {
    await page.goto('/login');
    await page.getByLabel('Username').fill(username as string);
    await page.getByLabel('Password').fill(password as string);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page).not.toHaveURL(/\/login/, { timeout: 30_000 });
  }

  async function readDocument(request: APIRequestContext): Promise<PermissionDocument> {
    const response = await request.get(`${apiUrl}/provision/${projectId}/permissions/`);
    expect(response.status(), await response.text()).toBe(200);
    return (await response.json()) as PermissionDocument;
  }

  async function runSql(request: APIRequestContext, query: string) {
    const response = await request.post(`${apiUrl}/schema/${projectId}/query`, { ...asStudio, data: { query } });
    expect.soft(response.status(), `${query}: ${await response.text()}`).toBeLessThan(300);
  }

  async function cleanUp(request: APIRequestContext) {
    const project = `${apiUrl}/provision/${projectId}`;
    const gone = (status: number) => status < 300 || status === 404;
    const untrack = await request.delete(`${project}/tracked-functions/public.${fn}`, asStudio);
    expect.soft(gone(untrack.status()), `untrack: ${untrack.status()}`).toBe(true);
    for (const [role, op] of [['anon', 'select'], ['user', 'insert']]) {
      const removed = await request.delete(`${project}/permissions/tables/public.${table}/roles/${role}/${op}`, asStudio);
      expect.soft(gone(removed.status()), `remove ${role} ${op}: ${removed.status()}`).toBe(true);
    }
    await runSql(request, `DROP FUNCTION IF EXISTS public.${fn}()`);
    await runSql(request, `DROP TABLE IF EXISTS public.${table}`);
  }

  test('create a public table, edit its grid, track a function, clean up', async ({ page }) => {
    await signIn(page);
    try {
      // 1. Create a table with "Anyone can read" checked.
      await page.goto(`/project/${projectId}/database/tables`);
      await page.getByTestId('new-table-btn').click();
      await page.getByTestId('table-name-input').fill(table);
      await page.getByText('+ Add column').click();
      await page.getByPlaceholder('e.g. email').nth(1).fill('body');
      await page.getByPlaceholder('e.g. text').nth(1).fill('text');
      const anon = page.getByLabel('Anyone can read (anon)');
      await expect(anon, 'the live user must be a Developer or above').toBeVisible();
      await anon.check();
      await page.getByTestId('create-table-submit').click();
      await expect(page.getByTestId('sidepanel')).toHaveCount(0, { timeout: 30_000 });

      // 2. The page and the API agree that anon may select it.
      await expect(page.getByTestId(`access-summary-${table}`)).toContainText('anon');
      const afterCreate = await readDocument(page.request);
      expect(afterCreate.tables).toContainEqual(
        expect.objectContaining({ table: `public.${table}`, role: 'anon', select: { filter: {}, columns: '*' } }),
      );

      // 3. The grid shows it, and gives `user` insert.
      await page.getByRole('link', { name: `API permissions of ${table}` }).click();
      await expect(page.getByRole('button', { name: 'anon select: Full access' })).toBeVisible();
      await page.getByRole('button', { name: 'user insert: No access' }).click();
      const panel = page.getByTestId('sidepanel');
      await panel.getByRole('button', { name: 'Without any checks' }).click();
      await panel.getByLabel('All columns').check();
      await panel.getByRole('button', { name: 'Save permission' }).click();
      await expect(page.getByRole('button', { name: 'user insert: Full access' })).toBeVisible();
      const afterGrid = await readDocument(page.request);
      expect(afterGrid.tables).toContainEqual(
        expect.objectContaining({ table: `public.${table}`, role: 'user', insert: { check: {}, columns: '*' } }),
      );

      // 4. A STABLE SETOF function is tracked as a query.
      await page.goto(`/project/${projectId}/database/functions`);
      await page.getByTestId('create-function-btn').click();
      await page.getByTestId('fn-name-input').fill(fn);
      await page.getByLabel('Language').selectOption('sql');
      await page.getByLabel('Returns').fill(`SETOF ${table}`); // the type validator takes no schema dot
      await page.getByLabel('Volatility').selectOption('STABLE');
      await page.getByTestId('fn-body-input').fill(`SELECT * FROM public.${table}`);
      await page.getByTestId('create-function-submit').click();
      const card = page.getByTestId(`fn-api-${fn}`);
      await card.getByRole('button', { name: 'Track', exact: true }).click({ timeout: 30_000 });
      await card.getByRole('button', { name: 'Track function' }).click();
      await expect(card).toContainText('Tracked · query', { timeout: 30_000 });
      const afterTrack = await readDocument(page.request);
      expect(afterTrack.functions).toContainEqual(
        expect.objectContaining({ function: `public.${fn}`, exposedAs: 'QUERY' }),
      );
    } finally {
      await cleanUp(page.request);
    }

    const afterCleanUp = await readDocument(page.request);
    expect(afterCleanUp.tables.filter((t) => t.table === `public.${table}`)).toEqual([]);
    expect(afterCleanUp.functions.filter((f) => f.function === `public.${fn}`)).toEqual([]);
  });
});
