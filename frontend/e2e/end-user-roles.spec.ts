import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints, mockProjectRole, SHOTS_DIR } from './helpers';

const INITIAL_END_USERS = {
  users: [
    { id: 1, email: 'alice@test.com', role: 'user', allowedRoles: ['user'] },
    { id: 2, email: 'bob@test.com', role: 'editor', allowedRoles: ['editor', 'user'] },
  ],
  total: 2,
};

test.describe('End-user roles', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('an admin sets an end user role and allowed roles', async ({ page }) => {
    await mockProjectRole(page, 'admin');
    const END_USERS = structuredClone(INITIAL_END_USERS);
    const puts: unknown[] = [];
    await page.route('**/api/projects/test-project/end-users/**', (route) => {
      const request = route.request();
      if (request.method() === 'PUT') {
        const body = JSON.parse(request.postData() ?? '{}');
        puts.push({ path: new URL(request.url()).pathname, body });
        END_USERS.users[0] = { ...END_USERS.users[0], ...body };
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(END_USERS.users[0]) });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(END_USERS) });
    });
    await page.goto('/project/test-project/auth/users');
    const alice = page.getByTestId('end-user-1');
    await expect(alice).toContainText('alice@test.com');
    await expect(page.getByTestId('end-user-roles')).toContainText('signs the user out of their existing sessions');

    await alice.getByRole('button', { name: 'Change role of alice@test.com' }).click();
    await alice.getByLabel('Role for alice@test.com').fill('anon');
    await alice.getByRole('button', { name: 'Save' }).click();
    await expect(alice.getByRole('alert')).toContainText('reserved');

    await alice.getByLabel('Role for alice@test.com').fill('editor');
    await alice.getByLabel('Allowed roles for alice@test.com').fill('editor, user');
    await page.screenshot({ path: `${SHOTS_DIR}/11-end-user-role-edit.png`, fullPage: true });
    await alice.getByRole('button', { name: 'Save' }).click();
    await expect(alice).toContainText('editor, user');
    expect(puts).toEqual([
      { path: '/api/projects/test-project/end-users/1/role', body: { role: 'editor', allowedRoles: ['editor', 'user'] } },
    ]);
    await page.screenshot({ path: `${SHOTS_DIR}/12-end-user-role-saved.png`, fullPage: true });
  });

  test('a developer is not offered end-user roles', async ({ page }) => {
    await mockProjectRole(page, 'developer');
    await page.goto('/project/test-project/auth/users');
    await expect(page.getByTestId('end-user-roles')).toContainText('Only project admins and owners');
    await expect(page.getByRole('button', { name: /Change role of/ })).toHaveCount(0);
  });
});
