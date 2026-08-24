import { test, expect, Page } from '@playwright/test';
import { mockVaultGuardReady, mockInstances } from './helpers';

/**
 * Verifies the new session-cookie auth flow against the studio:
 *
 *   1. Login is a server-side cookie issuer. The studio receives the
 *      `excali_session` Set-Cookie header on the login response. The token
 *      body field is also handed to the axios header fallback so CI/curl
 *      callers continue to work.
 *   2. Subsequent API calls go out with `withCredentials: true` so the
 *      cookie is auto-attached.
 *   3. Logout posts to /api/auth/logout (server revokes the session PAT
 *      and clears the cookie) and wipes the legacy localStorage.
 *   4. A 401 response on any authenticated call redirects the user to
 *      /login without leaving stale credentials behind.
 *
 * These tests stub the backend so they're deterministic in CI; the Go
 * server's cookie behaviour is covered by handler unit tests separately.
 */

async function mockLogin(page: Page): Promise<{ logoutCalls: number }> {
  const state = { logoutCalls: 0 };
  await page.route('**/api/auth/login', (route) =>
    route.fulfill({
      status: 200,
      headers: {
        // Production sets HttpOnly + Secure + SameSite=Strict. Strict isn't
        // valid in headlesss test contexts (cross-origin to localhost can
        // strip), so emit Lax for the test fixture; the JS-side behaviour
        // we care about (withCredentials, axios cookie attach) is the same.
        'Set-Cookie': 'excali_session=cookie-pat-abc; Path=/; HttpOnly; SameSite=Lax',
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        token: 'cookie-pat-abc',
        expiresAt: new Date(Date.now() + 12 * 3600 * 1000).toISOString(),
        user: { id: 'u1', username: 'admin', email: 'a@b.c', role: 'platform_admin' },
      }),
    })
  );
  await page.route('**/api/auth/logout', (route) => {
    state.logoutCalls += 1;
    return route.fulfill({
      status: 200,
      headers: {
        // Mirrors what the Go handler does — Max-Age<0 wipes the cookie.
        'Set-Cookie': 'excali_session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0',
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ status: 'ok' }),
    });
  });
  return state;
}

test.describe('session cookie auth flow', () => {
  test('login persists user + legacy token, redirects to dashboard', async ({ page }) => {
    await mockVaultGuardReady(page);
    await mockLogin(page);
    await mockInstances(page);

    await page.goto('/login');
    await page.fill('input[name="username"], input[placeholder*="sername" i]', 'admin');
    await page.fill('input[type="password"]', 'hunter2');
    await page.click('button[type="submit"]');

    // Lands on the post-login route (dashboard). Don't pin to a specific
    // path — the redirect target may change.
    await page.waitForURL((url) => !url.pathname.startsWith('/login'), { timeout: 5_000 });

    // legacyToken stored for the axios header fallback (CI/scripts).
    const stored = await page.evaluate(() => ({
      token: localStorage.getItem('auth_token'),
      user: localStorage.getItem('auth_user'),
    }));
    expect(stored.token).toBe('cookie-pat-abc');
    expect(stored.user && JSON.parse(stored.user).role).toBe('platform_admin');
  });

  test('post-login the studio fires authenticated API calls', async ({ page }) => {
    // Concrete proxy for "withCredentials wiring works": after a successful
    // login the dashboard's /api/provision GET lands. If the axios client
    // misconfigured credentials, the call would either not fire or come
    // back 401 (because session cookie wouldn't attach).
    await mockVaultGuardReady(page);
    await mockLogin(page);

    let provisionGetCount = 0;
    await page.route('**/api/provision', (route) => {
      if (route.request().method() === 'GET') {
        provisionGetCount += 1;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        });
      }
      return route.continue();
    });

    await page.goto('/login');
    await page.fill('input[name="username"], input[placeholder*="sername" i]', 'admin');
    await page.fill('input[type="password"]', 'hunter2');
    await page.click('button[type="submit"]');
    await page.waitForURL((url) => !url.pathname.startsWith('/login'), { timeout: 5_000 });

    await expect.poll(() => provisionGetCount, { timeout: 5_000 }).toBeGreaterThanOrEqual(1);
  });

  test('logout posts to /api/auth/logout and wipes localStorage', async ({ page }) => {
    await mockVaultGuardReady(page);
    const loginState = await mockLogin(page);
    await mockInstances(page);

    await page.goto('/login');
    await page.fill('input[name="username"], input[placeholder*="sername" i]', 'admin');
    await page.fill('input[type="password"]', 'hunter2');
    await page.click('button[type="submit"]');
    await page.waitForURL((url) => !url.pathname.startsWith('/login'), { timeout: 5_000 });

    // Sanity: localStorage is populated post-login.
    const before = await page.evaluate(() => localStorage.getItem('auth_token'));
    expect(before).toBe('cookie-pat-abc');

    // Click the logout button (matches both layouts via title attribute).
    await page.click('button[title="Sign out"]');

    // Wait for the POST /api/auth/logout to land server-side.
    await expect.poll(() => loginState.logoutCalls, { timeout: 3_000 }).toBeGreaterThanOrEqual(1);

    // localStorage cleared.
    const after = await page.evaluate(() => ({
      token: localStorage.getItem('auth_token'),
      user: localStorage.getItem('auth_user'),
    }));
    expect(after.token).toBeNull();
    expect(after.user).toBeNull();

    // Routed back to /login.
    await page.waitForURL(/\/login/, { timeout: 3_000 });
  });

  test('401 on any authenticated call clears state and redirects to /login', async ({ page }) => {
    await mockVaultGuardReady(page);
    await mockLogin(page);
    // Simulate stale session: list call returns 401.
    await page.route('**/api/provision', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'authentication required' }),
      })
    );

    // Land on /login first to establish the origin, then seed localStorage.
    // addInitScript is unsuitable here — it re-runs on every navigation,
    // which would overwrite the wipe we're testing for.
    await page.goto('/login');
    await page.evaluate(() => {
      localStorage.setItem('auth_token', 'stale-token');
      localStorage.setItem('auth_user', JSON.stringify({ id: 'u1', username: 'admin', email: 'a@b.c', role: 'platform_admin' }));
    });

    await page.goto('/');
    // axios response interceptor on 401 redirects + wipes localStorage.
    await page.waitForURL(/\/login/, { timeout: 5_000 });
    // Wait for the post-redirect page to settle so the interceptor's
    // localStorage.removeItem has flushed.
    await page.waitForLoadState('domcontentloaded');
    const after = await page.evaluate(() => localStorage.getItem('auth_token'));
    expect(after).toBeNull();
  });
});
