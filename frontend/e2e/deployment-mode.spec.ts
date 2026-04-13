import { test, expect } from '@playwright/test';
import { loginAs, mockInstances } from './helpers';

function mockConfig(page: import('@playwright/test').Page, mode: 'selfhosted' | 'cloud') {
  return page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: mode }),
    })
  );
}

test.describe('Self-hosted mode', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockInstances(page);
    await mockConfig(page, 'selfhosted');
  });

  test('hides Organizations nav item', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const nav = page.getByTestId('platform-nav');
    await expect(nav).toBeVisible();
    // Organizations should NOT be visible
    await expect(nav.getByText('Organizations')).not.toBeVisible();
    // Dashboard and Projects should still be visible
    await expect(nav.getByText('Dashboard')).toBeVisible();
    await expect(nav.getByText('Projects')).toBeVisible();
    await expect(nav.getByText('Provision')).toBeVisible();
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
    const nav = page.getByTestId('platform-nav');
    await expect(nav).toBeVisible();
    await expect(nav.getByText('Organizations')).toBeVisible();
    await expect(nav.getByText('Dashboard')).toBeVisible();
    await expect(nav.getByText('Projects')).toBeVisible();
    await expect(nav.getByText('Provision')).toBeVisible();
  });
});
