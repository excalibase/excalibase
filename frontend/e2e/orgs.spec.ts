import { test, expect } from '@playwright/test';
import { loginAs, mockCloudMode, mockVaultGuardReady } from './helpers';

const mockOrgs = [
  { id: 'org-1', name: 'Alice Corp', slug: 'alice-corp', tier: 'FREE', ownerId: '1', createdAt: '2026-01-01T00:00:00Z' },
  { id: 'org-2', name: 'Bob Inc', slug: 'bob-inc', tier: 'STANDARD', ownerId: '2', createdAt: '2026-01-01T00:00:00Z' },
];

const mockMembers = [
  { orgId: 'org-1', userId: '1', role: 'owner', email: 'admin@test.com', username: 'admin', createdAt: '2026-01-01T00:00:00Z' },
  { orgId: 'org-1', userId: '2', role: 'developer', email: 'bob@test.com', username: 'bob', createdAt: '2026-01-01T00:00:00Z' },
];

const mockPendingInvites = [
  { id: 1, orgId: 'org-1', email: 'newguy@test.com', role: 'developer', invitedBy: '1', createdAt: '2026-01-01T00:00:00Z' },
];

async function mockOrgEndpoints(page: import('@playwright/test').Page) {
  await page.route('**/api/orgs', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockOrgs) });
    }
    if (route.request().method() === 'POST') {
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ id: 'org-new', name: 'New Org', slug: 'new-org', tier: 'FREE', ownerId: '1' }),
      });
    }
    return route.continue();
  });

  await page.route('**/api/orgs/org-1', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockOrgs[0]) });
    }
    if (route.request().method() === 'PATCH') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...mockOrgs[0], tier: 'STANDARD' }) });
    }
    if (route.request().method() === 'DELETE') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'deleted' }) });
    }
    return route.continue();
  });

  await page.route('**/api/orgs/org-1/members', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockMembers) });
    }
    if (route.request().method() === 'POST') {
      return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ status: 'invited' }) });
    }
    return route.continue();
  });

  await page.route('**/api/orgs/org-1/invites', (route) => {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockPendingInvites) });
  });

  await page.route('**/api/provision', (route) => {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) });
  });
}

test.describe('Organizations', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockCloudMode(page);
    await mockOrgEndpoints(page);
  });

  test('shows org list page with orgs', async ({ page }) => {
    await page.goto('/orgs');
    await expect(page.getByRole('heading', { name: 'Organizations' })).toBeVisible();
    await expect(page.getByText('Alice Corp')).toBeVisible();
    await expect(page.getByText('Bob Inc')).toBeVisible();
  });

  test('create org form shows and auto-generates slug', async ({ page }) => {
    await page.goto('/orgs');
    await page.getByRole('button', { name: 'New Organization' }).click();
    await expect(page.getByText('Create Organization')).toBeVisible();

    await page.getByPlaceholder('My Company').fill('Test Org');
    await expect(page.getByPlaceholder('my-company')).toHaveValue('test-org');

    await page.getByRole('button', { name: 'Create' }).click();
    await page.waitForURL('**/orgs/org-new');
  });

  test('empty state shows create button', async ({ page }) => {
    await page.route('**/api/orgs', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
      }
      return route.continue();
    });
    await page.goto('/orgs');
    await expect(page.getByText('No organizations yet')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create Organization' })).toBeVisible();
  });

  test('org detail shows tabs', async ({ page }) => {
    await page.goto('/orgs/org-1');
    await expect(page.getByText('Alice Corp')).toBeVisible();
    await expect(page.getByText('alice-corp')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Projects' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Members' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Settings' })).toBeVisible();
  });

  test('members tab shows members with emails', async ({ page }) => {
    await page.goto('/orgs/org-1');
    await page.getByRole('button', { name: 'Members' }).click();
    await expect(page.getByText('admin@test.com')).toBeVisible();
    await expect(page.getByText('bob@test.com')).toBeVisible();
  });

  test('members tab shows pending invites', async ({ page }) => {
    await page.goto('/orgs/org-1');
    await page.getByRole('button', { name: 'Members' }).click();
    await expect(page.getByText('Pending Invites')).toBeVisible();
    await expect(page.getByText('newguy@test.com')).toBeVisible();
    await expect(page.getByText('Not registered yet')).toBeVisible();
  });

  test('invite member by email shows form', async ({ page }) => {
    await page.goto('/orgs/org-1');
    await page.getByRole('button', { name: 'Members' }).click();
    await page.getByRole('button', { name: 'Invite Member' }).click();
    await expect(page.getByPlaceholder('user@example.com')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Invite', exact: true })).toBeVisible();
    await expect(page.getByText('User must have a platform account')).toBeVisible();
  });

  test('settings tab shows tier and danger zone', async ({ page }) => {
    await page.goto('/orgs/org-1');
    await page.getByRole('button', { name: 'Settings' }).click();
    await expect(page.getByText('Organization Settings')).toBeVisible();
    await expect(page.getByText('Danger Zone')).toBeVisible();
  });

  test('sidebar has Organizations link', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('link', { name: 'Organizations' })).toBeVisible();
  });

  test('provision page has org dropdown', async ({ page }) => {
    await page.goto('/provision');
    await expect(page.getByText('Select organization...')).toBeAttached();
    await expect(page.getByText('Alice Corp (FREE)')).toBeAttached();
    await expect(page.getByRole('button', { name: /enterprise/i })).toHaveCount(0);
  });
});

test.describe('Registration', () => {
  test.beforeEach(async ({ page }) => {
    // /login and /register are inside VaultGuard but not AuthGuard, so
    // pre-auth tests still need the guard's status endpoints mocked.
    await mockVaultGuardReady(page);
  });

  test('register page renders', async ({ page }) => {
    await page.goto('/register');
    await expect(page.getByRole('heading', { name: 'Create Account' })).toBeVisible();
    await expect(page.getByPlaceholder('johndoe')).toBeVisible();
    await expect(page.getByPlaceholder('john@company.com')).toBeVisible();
    await expect(page.getByPlaceholder('Choose a password')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create Account' })).toBeVisible();
  });

  test('register page has login link', async ({ page }) => {
    await page.goto('/register');
    await expect(page.getByRole('link', { name: 'Sign in' })).toBeVisible();
  });

  test('login page has register link', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByRole('link', { name: 'Register' })).toBeVisible();
  });

  test('register form submits and redirects', async ({ page }) => {
    await page.route('**/api/auth/register', (route) => {
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ token: 'new-token', user: { id: '99', username: 'testuser', email: 'test@test.com', role: 'user' } }),
      });
    });
    // Mock orgs for redirect target
    await page.route('**/api/orgs', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });

    await page.goto('/register');
    await page.waitForLoadState('networkidle');
    await page.getByPlaceholder('johndoe').fill('testuser');
    await page.getByPlaceholder('john@company.com').fill('test@test.com');
    await page.getByPlaceholder('Choose a password').fill('Test123!');
    await page.getByRole('button', { name: 'Create Account' }).click();

    await page.waitForURL('**/orgs');
  });

  test('register shows error on duplicate', async ({ page }) => {
    await page.route('**/api/auth/register', (route) => {
      return route.fulfill({
        status: 409, contentType: 'application/json',
        body: JSON.stringify({ error: 'email already registered' }),
      });
    });

    await page.goto('/register');
    await page.getByPlaceholder('johndoe').fill('dup');
    await page.getByPlaceholder('john@company.com').fill('dup@test.com');
    await page.getByPlaceholder('Choose a password').fill('pass');
    await page.getByRole('button', { name: 'Create Account' }).click();

    await expect(page.getByText('email already registered')).toBeVisible();
  });
});

test.describe('Permission-based UI', () => {
  test('regular user does not see delete button on instances', async ({ page }) => {
    await loginAs(page, { id: '2', username: 'bob', email: 'bob@test.com', role: 'user' });
    await page.route('**/api/provision', (route) => {
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{ projectId: 'p1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'ns', status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: '2026-01-01' }]),
      });
    });
    await page.goto('/instances');
    await expect(page.getByText('p1')).toBeVisible();
    // Delete button should NOT be visible for regular user
    await expect(page.locator('button[class*="error"]')).toHaveCount(0);
  });

  test('platform admin sees delete button on instances', async ({ page }) => {
    await loginAs(page, { id: '1', username: 'admin', email: 'admin@test.com', role: 'platform_admin' });
    await page.route('**/api/provision', (route) => {
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{ projectId: 'p1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'ns', status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: '2026-01-01' }]),
      });
    });
    await page.goto('/instances');
    await expect(page.getByText('p1')).toBeVisible();
    // Delete button should be visible for platform_admin
    await expect(page.locator('button[class*="error"]')).toHaveCount(1);
  });

  test('developer user does not see invite/settings in org detail', async ({ page }) => {
    await loginAs(page, { id: '2', username: 'bob', email: 'bob@test.com', role: 'user' });

    const devMembers = [
      { orgId: 'org-1', userId: '2', role: 'developer', email: 'bob@test.com', username: 'bob' },
    ];

    await page.route('**/api/orgs', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 'org-1', name: 'SomeOrg', slug: 'some', tier: 'FREE', ownerId: '1' }]) });
      }
      return route.continue();
    });
    await page.route('**/api/orgs/org-1', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'org-1', name: 'SomeOrg', slug: 'some', tier: 'FREE', ownerId: '1' }) });
    });
    await page.route('**/api/orgs/org-1/members', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(devMembers) });
    });
    await page.route('**/api/orgs/org-1/invites', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });
    await page.route('**/api/provision', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });

    await page.goto('/orgs/org-1');
    await page.getByRole('button', { name: 'Members' }).click();

    // Developer should NOT see invite button
    await expect(page.getByRole('button', { name: 'Invite Member' })).toHaveCount(0);

    // Developer should NOT see settings tab content (no settings rendered)
    await page.getByRole('button', { name: 'Settings' }).click();
    await expect(page.getByText('Organization Settings')).toHaveCount(0);
    await expect(page.getByText('Danger Zone')).toHaveCount(0);
  });
});

test.describe('Org navigation', () => {
  test('click org in list navigates to detail', async ({ page }) => {
    await loginAs(page);
    await page.route('**/api/orgs', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 'org-nav', name: 'NavOrg', slug: 'nav-org', tier: 'FREE', ownerId: '1' }]) });
      }
      return route.continue();
    });
    await page.route('**/api/orgs/org-nav', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'org-nav', name: 'NavOrg', slug: 'nav-org', tier: 'FREE', ownerId: '1' }) });
    });
    await page.route('**/api/orgs/org-nav/members', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ orgId: 'org-nav', userId: '1', role: 'owner', email: 'admin@test.com', username: 'admin' }]) });
    });
    await page.route('**/api/orgs/org-nav/invites', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });
    await page.route('**/api/provision', (route) => {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' });
    });

    await page.goto('/orgs');
    await page.getByText('NavOrg').click();
    await page.waitForURL('**/orgs/org-nav');
    await expect(page.getByText('NavOrg')).toBeVisible();
  });
});
