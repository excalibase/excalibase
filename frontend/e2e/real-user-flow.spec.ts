/**
 * Real-user end-to-end Playwright spec — no API mocks.
 *
 * Drives the studio against a real provisioning backend running on
 * localhost:24005 (started by `tests/e2e-docker.sh` infrastructure or
 * manually via `STORAGE_PATH=/tmp/e2e-real DB_PATH=... excalibase-server`).
 *
 * Walks through what a brand-new operator does on first login:
 *   1. Land on /setup wizard (vault uninit, no admin)
 *   2. Initialize vault → see shares → continue
 *   3. Unseal with the share returned by init
 *   4. Register the platform admin
 *   5. Land on dashboard, see no projects yet
 *   6. (TODO if backend has Docker provisioner) provision a project
 *
 * Skipped by default — only runs when REAL_E2E=1 is set, since it
 * requires the full backend stack and modifies real persistent state.
 */

import { test, expect } from '@playwright/test';

const REAL_E2E = process.env.REAL_E2E === '1';
const API_BASE = process.env.E2E_API_URL || 'http://localhost:24005';

test.describe('Real-user studio flow (no mocks)', () => {
  test.skip(!REAL_E2E, 'set REAL_E2E=1 + start backend + reset state to enable');

  test('first-run: wizard → admin creation → dashboard → create project', async ({ page }) => {
    // Sanity: backend is reachable
    const healthRes = await page.request.get(`${API_BASE}/healthz`);
    expect(healthRes.status()).toBe(200);

    // 1. Landing on / should redirect to /setup since vault isn't initialized
    await page.goto('/');
    await expect(page).toHaveURL(/\/setup$/);
    await expect(page.getByTestId('vault-setup-init')).toBeVisible();

    // 2. Initialize vault with 1 share / threshold 1 (simplest path)
    await page.getByTestId('vault-init-shares').fill('1');
    await page.getByTestId('vault-init-threshold').fill('1');
    await page.getByTestId('vault-init-submit').click();

    // 3. Shares step — capture the share value, confirm we saved it,
    // continue to unseal
    await expect(page.getByTestId('vault-setup-shares')).toBeVisible();
    const shareCode = await page
      .locator('[data-testid^="vault-share-copy-"]')
      .first()
      .locator('xpath=preceding-sibling::code')
      .innerText();
    expect(shareCode.length).toBeGreaterThan(20);

    await page.getByTestId('vault-shares-confirm').check();
    await page.getByTestId('vault-shares-continue').click();

    // 4. The backend auto-unseals on init (Vault.Init sets the barrier
    // key in memory immediately) — so the wizard skips the unseal step
    // and goes straight from shares → admin. If the operator had restarted
    // the server before continuing, they'd have to unseal here.
    void shareCode; // captured above for completeness; not needed in this path

    // 5. Admin step
    await expect(page.getByTestId('vault-setup-admin')).toBeVisible();
    const username = `e2euser_${Date.now()}`;
    await page.getByTestId('admin-username').fill(username);
    await page.getByTestId('admin-email').fill(`${username}@e2e.local`);
    await page.getByTestId('admin-password').fill('Founder123!');
    await page.getByTestId('admin-submit').click();

    // 6. After admin created, should redirect away from /setup. We don't
    // assert exact post-redirect URL (depends on cloud vs self-hosted),
    // just that we're no longer on /setup and have a token.
    await expect(page).not.toHaveURL(/\/setup$/, { timeout: 10_000 });
    const tokenInStorage = await page.evaluate(() => localStorage.getItem('auth_token'));
    expect(tokenInStorage).toBeTruthy();
  });
});
