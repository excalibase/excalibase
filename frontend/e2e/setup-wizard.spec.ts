import { test, expect, Page } from '@playwright/test';

/**
 * Mock setup-status + vault-status endpoints with stateful values that
 * mutate as the wizard progresses, mirroring how the real backend would
 * react to init/unseal/register POSTs.
 */
type MockState = {
  initialized: boolean;
  sealed: boolean;
  threshold: number;
  shares: number;
  progress: number;
  hasAdmin: boolean;
};

async function mockSetupAPIs(page: Page, initial: MockState) {
  const state = { ...initial };

  await page.route('**/api/vault/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        initialized: state.initialized,
        sealed: state.sealed,
        threshold: state.threshold,
        shares: state.shares,
        progress: state.progress,
        type: 'shamir',
      }),
    })
  );

  await page.route('**/api/auth/setup-status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ hasAdmin: state.hasAdmin }),
    })
  );

  await page.route('**/api/vault/init', (route) => {
    state.initialized = true;
    state.sealed = true;
    state.threshold = 3;
    state.shares = 5;
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        shares: ['share-1', 'share-2', 'share-3', 'share-4', 'share-5'],
        threshold: 3,
      }),
    });
  });

  await page.route('**/api/vault/unseal', (route) => {
    state.progress += 1;
    if (state.progress >= state.threshold) {
      state.sealed = false;
      state.progress = 0;
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        sealed: state.sealed,
        progress: state.progress,
        threshold: state.threshold,
      }),
    });
  });

  await page.route('**/api/auth/register', (route) => {
    state.hasAdmin = true;
    return route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        token: 'pat-bootstrap-token',
        user: {
          id: 'u1',
          username: 'founder',
          email: 'founder@example.com',
          role: 'platform_admin',
        },
      }),
    });
  });

  // Stub config so AuthGuard doesn't hit the real /api/config
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'self-hosted' }),
    })
  );

  return state;
}

test.describe('Setup wizard', () => {
  test('redirects fresh install to /setup with init form visible', async ({ page }) => {
    await mockSetupAPIs(page, {
      initialized: false,
      sealed: true,
      threshold: 0,
      shares: 0,
      progress: 0,
      hasAdmin: false,
    });

    await page.goto('/');
    await expect(page).toHaveURL(/\/setup$/);
    await expect(page.getByTestId('vault-setup-init')).toBeVisible();
  });

  test('init step displays shares once and gates continue on confirmation', async ({ page }) => {
    await mockSetupAPIs(page, {
      initialized: false,
      sealed: true,
      threshold: 0,
      shares: 0,
      progress: 0,
      hasAdmin: false,
    });

    await page.goto('/setup');
    await expect(page.getByTestId('vault-setup-init')).toBeVisible();
    await page.getByTestId('vault-init-submit').click();

    // Shares step visible after init
    await expect(page.getByTestId('vault-setup-shares')).toBeVisible();
    // 5 share rows rendered
    for (let i = 0; i < 5; i++) {
      await expect(page.getByTestId(`vault-share-copy-${i}`)).toBeVisible();
    }
    // Continue is disabled until checkbox is ticked
    const continueBtn = page.getByTestId('vault-shares-continue');
    await expect(continueBtn).toBeDisabled();
    await page.getByTestId('vault-shares-confirm').check();
    await expect(continueBtn).toBeEnabled();
  });

  test('unseal step accepts shares and advances to admin step on completion', async ({ page }) => {
    await mockSetupAPIs(page, {
      initialized: true,
      sealed: true,
      threshold: 3,
      shares: 5,
      progress: 0,
      hasAdmin: false,
    });

    await page.goto('/setup');
    await expect(page.getByTestId('vault-setup-unseal')).toBeVisible();

    // Submit 3 shares — last one flips sealed→false and reveals admin step.
    // Wait for the input to clear between submissions instead of an arbitrary
    // timeout, so the test stays deterministic on slow CI.
    for (let i = 0; i < 3; i++) {
      await page.getByTestId('vault-unseal-input').fill(`share-${i}`);
      await page.getByTestId('vault-unseal-submit').click();
      if (i < 2) {
        await expect(page.getByTestId('vault-unseal-input')).toHaveValue('');
      }
    }

    await expect(page.getByTestId('vault-setup-admin')).toBeVisible();
  });

  test('admin step creates user and redirects to dashboard', async ({ page }) => {
    await mockSetupAPIs(page, {
      initialized: true,
      sealed: false,
      threshold: 1,
      shares: 1,
      progress: 0,
      hasAdmin: false,
    });

    await page.goto('/setup');
    await expect(page.getByTestId('vault-setup-admin')).toBeVisible();

    await page.getByTestId('admin-username').fill('founder');
    await page.getByTestId('admin-email').fill('founder@example.com');
    await page.getByTestId('admin-password').fill('Founder123!');
    // EXC-451: required field — the mocked /api/auth/register below accepts
    // any body, but the browser's own `required` validation blocks submit
    // until every field, including this one, has a value.
    await page.getByTestId('admin-setup-token').fill('e2e-mock-setup-token');
    await page.getByTestId('admin-submit').click();

    // Token persisted to localStorage by setAuth
    await page.waitForFunction(() => localStorage.getItem('auth_token') === 'pat-bootstrap-token');
    // Guard sees hasAdmin=true, redirects to /
    await expect(page).toHaveURL(/\/(?!setup).*$|^http:\/\/localhost:5173\/$/);
  });

  test('ready vault + admin redirects /setup back to /', async ({ page }) => {
    await mockSetupAPIs(page, {
      initialized: true,
      sealed: false,
      threshold: 1,
      shares: 1,
      progress: 0,
      hasAdmin: true,
    });

    // Pretend a user is already authenticated, otherwise AuthGuard sends to /login
    await page.addInitScript(() => {
      localStorage.setItem('auth_token', 'existing');
      localStorage.setItem(
        'auth_user',
        JSON.stringify({ id: 'u1', username: 'admin', email: 'a@t.com', role: 'platform_admin' })
      );
    });

    await page.goto('/setup');
    await expect(page).not.toHaveURL(/\/setup$/);
  });
});
