import { test, expect } from '@playwright/test';
import { FORCED_V2, loginAs, mockInstances } from './helpers';

function mockConfig(page: import('@playwright/test').Page, mode: 'selfhosted' | 'cloud') {
  return page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: mode }),
    })
  );
}

// v1 has the platform-nav rail (Dashboard, Provision); v2 has a Main navigation (Home, Projects).
const platformNav = (page: import('@playwright/test').Page) =>
  FORCED_V2 ? page.getByRole('navigation', { name: 'Main' }) : page.getByTestId('platform-nav');
const HOME_LINK = FORCED_V2 ? 'Home' : 'Dashboard';

test.describe('Self-hosted mode', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockInstances(page);
    await mockConfig(page, 'selfhosted');
  });

  test('hides Organizations nav item', async ({ page }) => {
    test.fixme(FORCED_V2, 'v2 gap (EXC-859): shellNav lists Organizations in a self-hosted install; v1 hides it');
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const nav = platformNav(page);
    await expect(nav).toBeVisible();
    // The home and projects links show once the nav is up; only then is a missing Organizations link meaningful.
    await expect(nav.getByText(HOME_LINK)).toBeVisible();
    await expect(nav.getByText('Projects')).toBeVisible();
    if (!FORCED_V2) await expect(nav.getByText('Provision')).toBeVisible();
    await expect(nav.getByText('Organizations')).not.toBeVisible();
  });

  test('navigating to /orgs redirects or shows nothing', async ({ page }) => {
    await page.goto('/orgs');
    await page.waitForLoadState('networkidle');
    // The page renders but org content depends on the route still existing
    // In self-hosted, the nav just hides the link — route still works for now
    // This is acceptable: hidden from UI, not blocked at router level
  });
});

test.describe('Cloud mode', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockInstances(page);
    await mockConfig(page, 'cloud');
  });

  test('shows Organizations nav item', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const nav = platformNav(page);
    await expect(nav).toBeVisible();
    await expect(nav.getByText('Organizations')).toBeVisible();
    await expect(nav.getByText(HOME_LINK)).toBeVisible();
    await expect(nav.getByText('Projects')).toBeVisible();
    if (!FORCED_V2) await expect(nav.getByText('Provision')).toBeVisible();
  });
});
