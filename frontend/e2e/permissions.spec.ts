import { test, expect } from '@playwright/test';
import {
  loginAs, mockProject, mockSchemaEndpoints, mockProjectRole, mockPermissionsApi, SHOTS_DIR,
  type PermissionDocumentMock,
} from './helpers';

function initialDoc(): PermissionDocumentMock {
  return {
    projectId: 'test-project',
    version: 1,
    tables: [
      { table: 'public.orders', role: 'anon', select: { filter: {}, columns: '*' } },
      { table: 'public.orders', role: 'user', select: { filter: { id: { _eq: 'X-Excalibase-User-Id' } }, columns: ['id'] } },
    ],
    functions: [],
    functionPermissions: [],
  };
}

test.describe('Table API permissions', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockProjectRole(page, 'developer');
  });

  test('the table list summarises access and links to the grid', async ({ page }) => {
    await mockPermissionsApi(page, initialDoc());
    await page.goto('/project/test-project/database/tables');
    await expect(page.getByTestId('access-summary-orders')).toContainText('anon');
    await expect(page.getByTestId('access-summary-orders')).toContainText('user');
    await page.screenshot({ path: `${SHOTS_DIR}/01-tables-access-summary.png`, fullPage: true });

    await page.getByRole('link', { name: 'API permissions of orders' }).click();
    await expect(page).toHaveURL(/database\/tables\/public\/orders\/permissions$/);
    await expect(page.getByTestId('perm-cell-anon-select')).toHaveText('Full access');
    await expect(page.getByTestId('perm-cell-user-select')).toHaveText('Custom');
    await expect(page.getByTestId('permissions-notes')).toContainText('bypasses every permission');
    await page.screenshot({ path: `${SHOTS_DIR}/02-permissions-grid.png`, fullPage: true });
  });

  test('edit, save and remove a permission from the grid', async ({ page }) => {
    const calls = await mockPermissionsApi(page, initialDoc());
    await page.goto('/project/test-project/database/tables/public/users/permissions');

    await page.getByRole('button', { name: 'user insert: No access' }).click();
    const panel = page.getByTestId('sidepanel');
    await expect(panel.getByTestId('check-error')).toContainText('Choose a row check');
    await panel.getByLabel('Row check', { exact: true }).fill('{"email": {"_eq": null}}');
    await expect(panel.getByTestId('check-error')).toContainText('null is not comparable');
    await expect(panel.getByRole('button', { name: 'Save permission' })).toBeDisabled();
    await page.screenshot({ path: `${SHOTS_DIR}/03-editor-validation.png`, fullPage: true });

    await panel.getByLabel('Owner column for Row check').selectOption('id');
    await panel.getByRole('button', { name: 'Owner only' }).click();
    await panel.getByLabel('email', { exact: true }).check();
    await panel.getByLabel('name', { exact: true }).check();
    await panel.getByRole('button', { name: 'Add preset' }).click();
    await panel.getByLabel('Preset column 1').selectOption('id');
    await panel.getByLabel('Preset value 1').fill('X-Excalibase-User-Id');
    await page.screenshot({ path: `${SHOTS_DIR}/04-editor-insert.png`, fullPage: true });
    await panel.getByRole('button', { name: 'Save permission' }).click();

    await expect(page.getByTestId('sidepanel')).toHaveCount(0);
    await expect(page.getByTestId('perm-cell-user-insert')).toHaveText('Custom');
    expect(calls).toContainEqual({
      method: 'PUT',
      path: '/api/provision/test-project/permissions/tables/public.users/roles/user/insert',
      body: { check: { id: { _eq: 'X-Excalibase-User-Id' } }, columns: ['email', 'name'], set: { id: 'X-Excalibase-User-Id' } },
    });

    await page.getByRole('button', { name: 'user insert: Custom' }).click();
    await page.getByRole('button', { name: 'Remove permission' }).click();
    await page.getByTestId('modal-confirm').click();
    await expect(page.getByTestId('perm-cell-user-insert')).toHaveText('No access');
    expect(calls.at(-1)).toMatchObject({
      method: 'DELETE',
      path: '/api/provision/test-project/permissions/tables/public.users/roles/user/insert',
    });
    await page.screenshot({ path: `${SHOTS_DIR}/05-grid-after-remove.png`, fullPage: true });
  });

  test('a custom role is added and given full select', async ({ page }) => {
    const calls = await mockPermissionsApi(page, initialDoc());
    await page.goto('/project/test-project/database/tables/public/users/permissions');
    await page.getByLabel('New role').fill('service');
    await page.getByRole('button', { name: 'Add role' }).click();
    await expect(page.getByTestId('add-role-error')).toContainText('bypasses permissions');
    await page.getByLabel('New role').fill('editor');
    await page.getByRole('button', { name: 'Add role' }).click();
    await page.getByRole('button', { name: 'editor select: No access' }).click();
    const panel = page.getByTestId('sidepanel');
    await panel.getByRole('button', { name: 'Without any checks' }).click();
    await panel.getByLabel('All columns').check();
    await panel.getByRole('button', { name: 'Save permission' }).click();
    await expect(page.getByTestId('perm-cell-editor-select')).toHaveText('Full access');
    expect(calls.at(-1)).toEqual({
      method: 'PUT',
      path: '/api/provision/test-project/permissions/tables/public.users/roles/editor/select',
      body: { filter: {}, columns: '*', allowAggregations: false },
    });
  });

  test('a server refusal is shown in the editor', async ({ page }) => {
    await mockPermissionsApi(page, initialDoc());
    await page.route('**/api/provision/test-project/permissions/tables/**', (route) =>
      route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'filter: "_exists._table": "public.Nope" is not schema.name', status: 400 }) }),
    );
    await page.goto('/project/test-project/database/tables/public/orders/permissions');
    await page.getByRole('button', { name: 'anon select: Full access' }).click();
    await page.getByTestId('sidepanel').getByRole('button', { name: 'Save permission' }).click();
    await expect(page.getByTestId('permission-save-error')).toContainText('is not schema.name');
    await page.screenshot({ path: `${SHOTS_DIR}/06-editor-server-error.png`, fullPage: true });
  });

  test('create a table readable by anyone', async ({ page }) => {
    const calls = await mockPermissionsApi(page, initialDoc());
    await page.goto('/project/test-project/database/tables');
    await page.getByTestId('new-table-btn').click();
    await page.getByTestId('table-name-input').fill('notes');
    const anon = page.getByLabel('Anyone can read (anon)');
    await expect(anon).not.toBeChecked();
    await expect(page.getByLabel('Signed-in users can read (user)')).not.toBeChecked();
    await anon.check();
    await page.screenshot({ path: `${SHOTS_DIR}/07-create-table-anon-read.png`, fullPage: true });
    await page.getByTestId('create-table-submit').click();
    await expect(page.getByTestId('sidepanel')).toHaveCount(0);
    expect(calls).toEqual([
      {
        method: 'PUT',
        path: '/api/provision/test-project/permissions/tables/public.notes/roles/anon/select',
        body: { filter: {}, columns: '*' },
      },
    ]);
  });

  test('a viewer cannot open the grid', async ({ page }) => {
    await mockProjectRole(page, 'viewer');
    await mockPermissionsApi(page, initialDoc());
    await page.goto('/project/test-project/database/tables/public/orders/permissions');
    await expect(page.getByText('developers and above')).toBeVisible();
    await expect(page.getByTestId('perm-cell-anon-select')).toHaveCount(0);
  });
});
