import { test, expect, type Page } from '@playwright/test';
import { loginAs } from './helpers';

// EXC-762 step 2: Home and the Projects list of the console layout (studio_v2).

const DAY = 86_400_000;
const iso = (days: number) => new Date(Date.now() + days * DAY).toISOString();
const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

const ORGS = [{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: '1', role: 'owner' }];

function project(projectId: string, extra: object = {}) {
  return {
    projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'ns', status: 'ACTIVE',
    currentStage: 'COMPLETED', host: 'localhost', port: '5432', databaseName: 'db', backupEnabled: false,
    createdAt: iso(-30), updatedAt: iso(-30), ...extra,
  };
}

const TOKENS = [
  { id: 't1', tokenPrefix: 'exc_a', name: 'old-ci', scopes: 'read', createdAt: iso(-90), expiresAt: iso(-3) },
  { id: 't2', tokenPrefix: 'exc_b', name: 'scoped', scopes: 'read', projectId: 'reading-list', createdAt: iso(-10), expiresAt: iso(30) },
  { id: 't3', tokenPrefix: 'exc_c', name: 'all-projects', scopes: 'read', createdAt: iso(-10), expiresAt: iso(120) },
];

async function mockConsole(page: Page, projects: object[]) {
  let current = projects;
  await loginAs(page);
  await page.route('**/api/config', (route) => route.fulfill(json({
    deploymentMode: 'cloud', appHosting: true, sites: true,
    features: { studio_v2: true, dballowlist: true, mcp: true },
  })));
  await page.route('**/api/orgs', (route) => route.fulfill(json(ORGS)));
  await page.route('**/api/provision', (route) => route.fulfill(json(current)));
  await page.route('**/api/auth/tokens', (route) => route.fulfill(json(TOKENS)));
  await page.route('**/api/projects/*/apps/', (route) => route.fulfill(json([])));
  await page.route('**/api/projects/*/sites/', (route) => route.fulfill(json([])));
  await page.route('**/api/projects/reading-list/apps/', (route) => route.fulfill(json([{ id: 'a1', projectId: 'reading-list', name: 'api', status: 'ACTIVE' }])));
  await page.route('**/api/projects/*/db-endpoint/allowed-sources', (route) => {
    const open = route.request().url().includes('/open-db/');
    return route.fulfill(json({ projectId: 'x', access: open ? 'any' : 'listed', sources: [], max: 10, publicEnabled: open, canChange: true, callerAddress: '1.2.3.4' }));
  });
  await page.route('**/api/provision/*/resume', (route) => {
    current = current.map((p) => ((p as { status: string }).status === 'PAUSED' ? { ...p, status: 'ACTIVE' } : p));
    return route.fulfill(json({ projectId: 'mcp-test', status: 'ACTIVE' }));
  });
}

const SEED = [
  project('reading-list', { projectName: 'Reading list', postgresVersion: '16', lastSeenAt: iso(-1) }),
  project('open-db', { projectName: 'Open one' }),
  project('mcp-test', { status: 'PAUSED', pauseReason: 'idle_7d', lastActiveAt: iso(-5) }),
  project('blank', { noDatabase: true }),
];

test.describe('Studio v2 Home', () => {
  test('renders the widgets from seeded data', async ({ page }) => {
    await mockConsole(page, SEED);
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Home' })).toBeVisible();
    await expect(page.getByText('Acme').first()).toBeVisible();
    for (const name of ['Recently visited', 'Health', 'Credits & usage', 'Security', 'Build something']) {
      await expect(page.getByRole('region', { name })).toBeVisible();
    }
    await expect(page.getByRole('region', { name: 'Latest activity' })).toHaveCount(0);
    const health = page.getByRole('region', { name: 'Health' });
    await expect(health).toContainText('Running');
    const security = page.getByRole('region', { name: 'Security' });
    await expect(security.getByText('Databases open to the internet').locator('xpath=following-sibling::span')).toHaveText('1');
    await expect(security.getByText('Keys with access to every project').locator('xpath=following-sibling::span')).toHaveText('1');
    await expect(page.getByRole('region', { name: 'Credits & usage' }).getByRole('link', { name: /usage details/i })).toHaveAttribute('href', '/usage');
    await expect(page.getByRole('region', { name: 'Build something' }).getByRole('link', { name: /container image/ })).toBeVisible();
  });

  test('the attention bar lists a paused project and Resume brings it back', async ({ page }) => {
    await mockConsole(page, SEED);
    await page.goto('/');
    const bar = page.getByRole('region', { name: 'Needs your attention' });
    await expect(bar).toContainText('mcp-test');
    await expect(bar).toContainText('old-ci');
    await expect(bar.getByRole('link', { name: 'Delete mcp-test' })).toHaveAttribute('href', '/project/mcp-test/settings');
    await bar.getByRole('button', { name: 'Resume mcp-test' }).click();
    await expect(bar.getByRole('button', { name: 'Resume mcp-test' })).toHaveCount(0);
  });

  test('the bar is gone when nothing needs attention', async ({ page }) => {
    await mockConsole(page, [project('reading-list')]);
    await page.route('**/api/auth/tokens', (route) => route.fulfill(json([])));
    await page.goto('/');
    await expect(page.getByRole('region', { name: 'Health' })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Needs your attention' })).toHaveCount(0);
  });

  test('Recently visited remembers pages opened in this browser', async ({ page }) => {
    await mockConsole(page, SEED);
    await page.goto('/account/tokens');
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Home' }).click();
    await expect(page.getByRole('region', { name: 'Recently visited' }).getByRole('link', { name: /Access keys/ })).toBeVisible();
  });

  test('widgets can be hidden', async ({ page }) => {
    await mockConsole(page, SEED);
    await page.goto('/');
    await page.getByRole('button', { name: /customize widgets/i }).click();
    await page.getByRole('checkbox', { name: 'Security' }).uncheck();
    await expect(page.getByRole('region', { name: 'Security' })).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole('region', { name: 'Health' })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Security' })).toHaveCount(0);
  });

  test('works at phone width without sideways page scroll', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 800 });
    await mockConsole(page, SEED);
    await page.goto('/');
    await expect(page.getByRole('region', { name: 'Health' })).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });
});

test.describe('Studio v2 Projects list', () => {
  const MANY = [
    ...SEED,
    ...Array.from({ length: 10 }, (_, i) => project(`extra-${String(i).padStart(2, '0')}`, { lastSeenAt: iso(-10 - i) })),
  ];

  test('shows what each project contains, its status and an action', async ({ page }) => {
    await mockConsole(page, SEED);
    await page.goto('/instances');
    await expect(page.getByText('1–4 of 4')).toBeVisible();
    const row = (name: string) => page.getByRole('row').filter({ hasText: name });
    await expect(row('Reading list')).toContainText('Postgres 16 + 1 app');
    await expect(row('Reading list')).toContainText('Running');
    await expect(row('mcp-test')).toContainText('Paused · idle');
    await expect(row('mcp-test').getByRole('button', { name: 'Resume mcp-test' })).toBeVisible();
    await expect(row('blank')).toContainText('No services yet');
    await expect(row('blank').getByRole('link', { name: 'Add service blank' })).toBeVisible();
    await expect(page.getByText(/A paused project keeps its data/)).toBeVisible();
  });

  test('filters by name, status and contents', async ({ page }) => {
    await mockConsole(page, SEED);
    await page.goto('/instances');
    await page.getByRole('searchbox', { name: /filter projects/i }).fill('reading');
    await expect(page.getByRole('row')).toHaveCount(2);
    await page.getByRole('searchbox', { name: /filter projects/i }).fill('');
    await page.getByRole('combobox', { name: 'Status' }).selectOption('paused');
    await expect(page.getByRole('row')).toHaveCount(2);
    await expect(page.getByRole('link', { name: /^mcp-test/ })).toBeVisible();
    await page.getByRole('combobox', { name: 'Status' }).selectOption('');
    await page.getByRole('combobox', { name: 'Contains' }).selectOption('app');
    await expect(page.getByRole('row')).toHaveCount(2);
    await expect(page.getByRole('link', { name: /^Reading list/ })).toBeVisible();
  });

  test('sorts and pages', async ({ page }) => {
    await mockConsole(page, MANY);
    await page.goto('/instances');
    await expect(page.getByText('1–10 of 14')).toBeVisible();
    await page.getByRole('button', { name: 'Next page' }).click();
    await expect(page.getByText('11–14 of 14')).toBeVisible();
    await expect(page.getByRole('row')).toHaveCount(5);
    await page.getByRole('button', { name: 'Previous page' }).click();
    await page.getByRole('button', { name: 'Project' }).click();
    await expect(page.getByRole('columnheader', { name: 'Project' })).toHaveAttribute('aria-sort', 'ascending');
    await expect(page.getByRole('row').nth(1)).toContainText('blank');
    await page.getByRole('button', { name: 'Project' }).click();
    await expect(page.getByRole('columnheader', { name: 'Project' })).toHaveAttribute('aria-sort', 'descending');
  });
});
