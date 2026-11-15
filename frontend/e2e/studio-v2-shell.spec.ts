import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-762 step 1: the console layout behind the studio_v2 flag.

const ORGS = [
  { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' },
  { id: 'org-other', name: 'Not Mine', slug: 'nm', tier: 'FREE', ownerId: '9' },
];

function project(projectId: string, extra: object = {}) {
  return {
    projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'ns', status: 'ACTIVE',
    currentStage: 'COMPLETED', host: 'localhost', port: '5432', databaseName: 'db', backupEnabled: false,
    createdAt: '2026-10-01T00:00:00Z', updatedAt: '2026-10-01T00:00:00Z', ...extra,
  };
}

const PROJECTS = [
  project('reading-list', { projectName: 'Reading list' }),
  project('mcp-test', { status: 'PAUSED', tier: 'STANDARD' }),
  project('foreign-app', { orgId: 'org-other' }),
];

async function mockConsole(page: Page, flag: boolean) {
  await loginAs(page);
  await page.route('**/api/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'cloud', features: { studio_v2: flag } }) }));
  await page.route('**/api/orgs', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ORGS) }));
  await page.route('**/api/provision', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(PROJECTS) }));
}

test.describe('Studio v2 shell', () => {
  test('flag off keeps the current layout', async ({ page }) => {
    await mockConsole(page, false);
    await page.goto('/instances');
    await expect(page.getByTestId('platform-nav')).toBeVisible();
    await expect(page.getByTestId('studio-shell')).toHaveCount(0);
  });

  test('flag on shows the top bar, the left nav and the org switcher', async ({ page }) => {
    await mockConsole(page, true);
    await page.goto('/');
    await expect(page.getByTestId('studio-shell')).toBeVisible();
    const nav = page.getByRole('navigation', { name: 'Main' });
    for (const name of ['Home', 'Projects', 'Usage & credits', 'Access tokens', 'Members & roles', 'Organization settings', 'Docs']) {
      await expect(nav.getByRole('link', { name })).toBeVisible();
    }
    const org = page.getByRole('button', { name: /organization: acme/i });
    await expect(org).toContainText('Free');
    await org.click();
    await expect(page.getByRole('option')).toHaveCount(1);
    await expect(page.getByText('Not Mine')).toHaveCount(0);
  });

  test('Ctrl+K opens the palette and finds my projects, not other orgs', async ({ page }) => {
    await mockConsole(page, true);
    await page.goto('/');
    await expect(page.getByTestId('studio-shell')).toBeVisible();
    await page.keyboard.press('Control+k');
    const dialog = page.getByRole('dialog', { name: 'Search' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText('Reading list')).toBeVisible();
    await expect(dialog.getByText('foreign-app')).toHaveCount(0);
    await dialog.getByRole('combobox').fill('mcp');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/project\/mcp-test$/);
    await expect(dialog).toBeHidden();
  });

  test('the palette reaches a navigation destination and Escape closes it', async ({ page }) => {
    await mockConsole(page, true);
    await page.goto('/');
    await page.getByRole('button', { name: 'Search' }).click();
    const dialog = page.getByRole('dialog', { name: 'Search' });
    await dialog.getByRole('combobox').fill('access tokens');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/account\/tokens$/);
    await expect(dialog).toBeHidden();
    await page.keyboard.press('Control+k');
    await expect(dialog).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
  });

  test('Projects uses the shared table: filter, then status filter', async ({ page }) => {
    await mockConsole(page, true);
    await page.goto('/instances');
    await expect(page.getByText('1–2 of 2')).toBeVisible();
    await expect(page.getByRole('link', { name: /foreign-app/ })).toHaveCount(0);
    await page.getByRole('searchbox', { name: /filter projects/i }).fill('reading');
    await expect(page.getByRole('row')).toHaveCount(2);
    await page.getByRole('searchbox', { name: /filter projects/i }).fill('');
    await page.getByRole('combobox', { name: 'Plan' }).selectOption('STANDARD');
    await expect(page.getByRole('row')).toHaveCount(2);
    await expect(page.getByRole('link', { name: /mcp-test/ })).toBeVisible();
  });

  test('a 403 on the project list is an error, not an empty list', async ({ page }) => {
    await mockConsole(page, true);
    await page.route('**/api/provision', (route) => route.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"forbidden"}' }));
    await page.goto('/instances');
    await expect(page.getByRole('alert')).toContainText(/do not have access/i);
    await expect(page.getByText(/no projects yet/i)).toHaveCount(0);
  });

  test('a project shows Org / Project and the tab bar', async ({ page }) => {
    await mockConsole(page, true);
    await page.route('**/api/provision/reading-list', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(PROJECTS[0]) }));
    await page.goto('/project/reading-list');
    await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Acme');
    await expect(page.getByRole('button', { name: 'Project: Reading list' })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Project' }).getByRole('link', { name: 'Services' })).toHaveAttribute('aria-current', 'page');
  });
});
