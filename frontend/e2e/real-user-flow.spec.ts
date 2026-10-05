/**
 * Real-user end-to-end Playwright spec: no API mocks, a fresh platform.
 *
 * Env contract (skipped unless all three required ones are set):
 *   REAL_E2E=1         opt-in; the run claims the platform's first admin, which
 *                      can happen once per install.
 *   STUDIO_LIVE_URL    the Studio origin the control plane trusts for cookie
 *                      writes (its STUDIO_URL), serving /api from provisioning.
 *                      Also the Playwright baseURL; no dev server is started.
 *   SETUP_TOKEN        the one-time first-admin token (EXC-451); on k8s it is
 *                      `kubectl -n excalibase-platform get secret platform-setup-token
 *                      -o jsonpath='{.data.token}' | base64 -d`.
 *   REAL_E2E_ADMIN_USERNAME / _EMAIL / _PASSWORD   optional; the first admin
 *                      this run creates (defaults: e2eadmin_<time>, <user>@e2e.local,
 *                      a random password). The account stays on the platform.
 *   REAL_E2E_UNSEAL_SHARE  only for an install whose vault is initialized but
 *                      sealed; otherwise unused.
 *
 * What a brand-new operator does: lands on /setup, which asks for the admin
 * first (vault init/unseal need an operator credential); a wrong setup token
 * is refused; the right one creates a signed-in platform_admin. Then the vault:
 * on k8s the bootstrap Job has already initialized and unsealed it, so the
 * wizard ends; elsewhere the operator initializes (1 share) and unseals here.
 */

import { randomBytes } from 'node:crypto';
import { test, expect, type Page } from '@playwright/test';

const REAL_E2E = process.env.REAL_E2E === '1';
const studioUrl = process.env.STUDIO_LIVE_URL;
const setupToken = process.env.SETUP_TOKEN;
const apiUrl = studioUrl ? `${studioUrl.replace(/\/$/, '')}/api` : '';

const username = process.env.REAL_E2E_ADMIN_USERNAME ?? `e2eadmin_${Date.now()}`;
const email = process.env.REAL_E2E_ADMIN_EMAIL ?? `${username}@e2e.local`;
const password = process.env.REAL_E2E_ADMIN_PASSWORD ?? `Aa1${randomBytes(18).toString('hex')}`;

interface VaultStatus {
  initialized: boolean;
  sealed: boolean;
}

async function vaultStatus(page: Page): Promise<VaultStatus> {
  const res = await page.request.get(`${apiUrl}/vault/status`);
  expect(res.status()).toBe(200);
  return res.json();
}

async function initVault(page: Page): Promise<string> {
  await expect(page.getByTestId('vault-setup-init')).toBeVisible();
  await page.getByTestId('vault-init-shares').fill('1');
  await page.getByTestId('vault-init-threshold').fill('1');
  await page.getByTestId('vault-init-submit').click();

  await expect(page.getByTestId('vault-setup-shares')).toBeVisible();
  const share = await page
    .getByTestId('vault-share-copy-0')
    .locator('xpath=preceding-sibling::code')
    .innerText();
  expect(share.length).toBeGreaterThan(20);
  await page.getByTestId('vault-shares-confirm').check();
  await page.getByTestId('vault-shares-continue').click();
  return share;
}

async function unsealVault(page: Page, share: string | undefined) {
  if (!share) throw new Error('the vault is sealed: set REAL_E2E_UNSEAL_SHARE');
  await expect(page.getByTestId('vault-setup-unseal')).toBeVisible();
  await page.getByTestId('vault-unseal-input').fill(share);
  await page.getByTestId('vault-unseal-submit').click();
}

test.describe('Real-user studio flow (no mocks)', () => {
  test.skip(
    !REAL_E2E || !studioUrl || !setupToken,
    'set REAL_E2E=1, STUDIO_LIVE_URL and SETUP_TOKEN against a fresh platform to run',
  );
  test.setTimeout(120_000);

  test('first run: admin with the setup token, then the vault, then signed in', async ({ page }) => {
    const setup = await page.request.get(`${apiUrl}/auth/setup-status`);
    expect(setup.status()).toBe(200);
    expect(await setup.json(), 'the platform already has an admin; this needs a fresh install').toEqual({
      hasAdmin: false,
    });

    // A fresh install sends every page to the wizard, which asks for the admin first.
    await page.goto('/');
    await expect(page).toHaveURL(/\/setup$/);
    await expect(page.getByTestId('vault-setup-admin')).toBeVisible();
    await expect(page.getByTestId('vault-setup-init')).toHaveCount(0);

    await page.getByTestId('admin-username').fill(username);
    await page.getByTestId('admin-email').fill(email);
    await page.getByTestId('admin-password').fill(password);
    await page.getByTestId('admin-password-confirm').fill(password);

    // Anyone can reach /setup before the operator does; only the token holder may claim it.
    await page.getByTestId('admin-setup-token').fill('not-the-setup-token');
    const refused = page.waitForResponse((r) => r.url().endsWith('/api/auth/register'));
    await page.getByTestId('admin-submit').click();
    expect((await refused).status()).toBe(403);
    await expect(page.getByTestId('vault-setup-admin')).toBeVisible();
    await expect(page).toHaveURL(/\/setup$/);

    await page.getByTestId('admin-setup-token').fill(setupToken as string);
    const claimed = page.waitForResponse((r) => r.url().endsWith('/api/auth/register'));
    await page.getByTestId('admin-submit').click();
    expect((await claimed).status()).toBe(201);
    await expect(page.getByTestId('vault-setup-admin')).toHaveCount(0, { timeout: 15_000 });

    // Signed in with a cookie no script can read, as the platform admin.
    const session = (await page.context().cookies()).find((c) => c.name === 'excali_session');
    expect(session?.httpOnly).toBe(true);
    expect(await page.evaluate(() => localStorage.getItem('auth_token'))).toBeNull();
    const me = await page.request.get(`${apiUrl}/auth/me`);
    expect(me.status()).toBe(200);
    expect(await me.json()).toMatchObject({ username, role: 'platform_admin' });

    const vault = await vaultStatus(page);
    let share = process.env.REAL_E2E_UNSEAL_SHARE;
    if (!vault.initialized) {
      await page.goto('/setup');
      share = await initVault(page);
    }
    if ((await vaultStatus(page)).sealed) {
      await page.goto('/setup');
      await unsealVault(page, share);
    }

    expect(await vaultStatus(page)).toMatchObject({ initialized: true, sealed: false });
    await expect(page).not.toHaveURL(/\/setup$/, { timeout: 15_000 });
    await expect(page).not.toHaveURL(/\/login$/);
    const after = await page.request.get(`${apiUrl}/auth/setup-status`);
    expect(await after.json()).toEqual({ hasAdmin: true });
  });
});
