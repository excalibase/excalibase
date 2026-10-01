import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints, mockProjectRole, mockPermissionsApi, SHOTS_DIR } from './helpers';

const FUNCTIONS = [
  { name: 'search_notes', schema: 'public', language: 'sql', returnType: 'SETOF notes', argTypes: 'q text, session jsonb', volatility: 'STABLE', definition: 'CREATE FUNCTION public.search_notes(q text, session jsonb) ... STABLE' },
  { name: 'note_count', schema: 'public', language: 'sql', returnType: 'integer', argTypes: '', volatility: 'STABLE', definition: 'SELECT count(*) FROM notes' },
];

test.describe('Function tracking', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockProjectRole(page, 'developer');
    await page.route('**/api/schema/test-project/functions*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(FUNCTIONS) }),
    );
  });

  test('track a function, allow a role, then untrack it', async ({ page }) => {
    const calls = await mockPermissionsApi(
      page,
      { projectId: 'test-project', version: 1, tables: [], functions: [], functionPermissions: [] },
      { trackAnswer: () => ({ status: 201, body: { securityDefiner: true } }) },
    );
    await page.goto('/project/test-project/database/functions');
    const card = page.getByTestId('fn-api-search_notes');
    await expect(card).toContainText('Not in the API');
    await card.getByRole('button', { name: 'Track', exact: true }).click();
    await expect(card).toContainText('Exposed as a query');
    await card.getByLabel('Infer permissions from select').uncheck();
    await card.getByLabel('Session argument').selectOption('session');
    await page.screenshot({ path: `${SHOTS_DIR}/08-track-function-options.png`, fullPage: true });
    await card.getByRole('button', { name: 'Track function' }).click();

    await expect(card).toContainText('Tracked · query');
    await expect(card).toContainText("runs with the owner's privileges");
    await expect(card).toContainText('Session argument: session');
    await expect(card).not.toContainText('as the caller');
    expect(calls[0]).toEqual({
      method: 'POST',
      path: '/api/provision/test-project/tracked-functions/',
      body: { function: 'public.search_notes', inferPermissions: false, sessionArgument: 'session' },
    });

    await card.getByLabel('Role allowed to call search_notes').fill('editor');
    await card.getByRole('button', { name: 'Allow role' }).click();
    await expect(card.getByRole('button', { name: 'Remove editor from search_notes' })).toBeVisible();
    await page.screenshot({ path: `${SHOTS_DIR}/09-tracked-function.png`, fullPage: true });

    await card.getByRole('button', { name: 'Untrack' }).click();
    await page.getByTestId('modal-confirm').click();
    await expect(card).toContainText('Not in the API');
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /api/provision/test-project/tracked-functions/',
      'PUT /api/provision/test-project/function-permissions/public.search_notes/roles/editor',
      'DELETE /api/provision/test-project/tracked-functions/public.search_notes',
    ]);
  });

  test('an untrackable function shows the server reason', async ({ page }) => {
    await mockPermissionsApi(
      page,
      { projectId: 'test-project', version: 1, tables: [], functions: [], functionPermissions: [] },
      { trackAnswer: () => ({ status: 400, body: { error: 'the function must return rows of a table or view (SETOF <table> or <table>)', status: 400 } }) },
    );
    await page.goto('/project/test-project/database/functions');
    const card = page.getByTestId('fn-api-note_count');
    await card.getByRole('button', { name: 'Track', exact: true }).click();
    await card.getByRole('button', { name: 'Track function' }).click();
    await expect(card.getByRole('alert')).toContainText('must return rows of a table or view');
    await page.screenshot({ path: `${SHOTS_DIR}/10-untrackable-function.png`, fullPage: true });
  });
});
