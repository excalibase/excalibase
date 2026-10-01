import { test, expect, type Browser, type Page } from '@playwright/test';

// Runs against a real Studio and control plane, never against mocks:
//   STUDIO_LIVE_URL=http://localhost:5199 STUDIO_LIVE_USER=admin STUDIO_LIVE_PASSWORD=... \
//   STUDIO_LIVE_OTHER_USER=dev2 STUDIO_LIVE_OTHER_PASSWORD=... \
//   npx playwright test e2e/live/access-tokens.live.spec.ts
// STUDIO_LIVE_URL must be the origin the control plane trusts (its STUDIO_URL).
// STUDIO_LIVE_HIDDEN_TOKEN_NAME, when set, names a service-account token that
// must never appear on a person's page. STUDIO_LIVE_SHOTS is where screenshots go.

const studioUrl = process.env.STUDIO_LIVE_URL;
const username = process.env.STUDIO_LIVE_USER;
const password = process.env.STUDIO_LIVE_PASSWORD;
const otherUser = process.env.STUDIO_LIVE_OTHER_USER;
const otherPassword = process.env.STUDIO_LIVE_OTHER_PASSWORD;
const hiddenTokenName = process.env.STUDIO_LIVE_HIDDEN_TOKEN_NAME;
const shots = process.env.STUDIO_LIVE_SHOTS;
const apiUrl = studioUrl ? `${studioUrl.replace(/\/$/, '')}/api` : '';
const suffix = Date.now().toString(36);

async function signIn(page: Page, user: string, secret: string) {
  await page.goto('/login');
  await page.getByLabel('Username').fill(user);
  await page.getByLabel('Password').fill(secret);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).not.toHaveURL(/\/login/, { timeout: 30_000 });
}

async function shot(page: Page, name: string) {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true });
}

async function createToken(page: Page, name: string, access: 'read' | 'write', expires: string): Promise<string> {
  await page.getByLabel('Token name').fill(name);
  await page.getByLabel('Access').selectOption(access);
  await page.getByLabel('Expires').selectOption(expires);
  await page.getByRole('button', { name: 'Create token' }).click();
  const notice = page.getByTestId('new-access-token');
  await expect(notice).toBeVisible();
  return (await notice.locator('code').innerText()).trim();
}

async function newSignedInPage(browser: Browser, user: string, secret: string) {
  const context = await browser.newContext({ baseURL: studioUrl });
  const page = await context.newPage();
  await signIn(page, user, secret);
  return page;
}

test.describe('Access tokens against a live Studio', () => {
  test.skip(!studioUrl || !username || !password, 'set STUDIO_LIVE_URL, STUDIO_LIVE_USER and STUDIO_LIVE_PASSWORD to run');
  test.setTimeout(180_000);

  test('create (shown once, copied), use, refuse wider, revoke with confirmation', async ({ page, context, request }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: studioUrl });
    await signIn(page, username as string, password as string);
    await page.getByTestId('platform-nav').getByRole('link', { name: 'Access tokens' }).click();
    await expect(page.getByRole('heading', { name: 'Access tokens' })).toBeVisible();

    const name = `live-ro-${suffix}`;
    const secret = await createToken(page, name, 'read', '30d');
    expect(secret).toMatch(/^excb_[0-9a-f]{64}$/);
    await page.getByRole('button', { name: 'Copy token' }).click();
    await expect(page.getByText('Copied to the clipboard.')).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(secret);
    await shot(page, '01-created-shown-once');

    await page.getByRole('button', { name: 'I have saved it' }).click();
    await page.reload();
    const row = page.getByRole('row').filter({ hasText: name });
    await expect(row).toContainText('Read only');
    await expect(row.getByTestId('token-last-used')).toHaveText('Never');
    expect(await page.content()).not.toContain(secret);

    const bearer = { headers: { Authorization: `Bearer ${secret}` } };
    expect((await request.get(`${apiUrl}/auth/me`, bearer)).status()).toBe(200);
    const wider = await request.post(`${apiUrl}/auth/tokens`, { ...bearer, data: { name: `wider-${suffix}`, scopes: ['write'] } });
    expect(wider.status(), await wider.text()).toBe(403);
    const unscoped = await request.post(`${apiUrl}/auth/tokens`, { ...bearer, data: { name: `all-${suffix}` } });
    expect(unscoped.status(), await unscoped.text()).toBe(403);

    await page.reload();
    await expect(row.getByTestId('token-last-used')).not.toHaveText('Never');
    if (hiddenTokenName) await expect(page.getByRole('row').filter({ hasText: hiddenTokenName })).toHaveCount(0);
    await expect(page.getByRole('row').filter({ hasText: `wider-${suffix}` })).toHaveCount(0);
    await shot(page, '02-listed-last-used');

    await row.getByRole('button', { name: 'Revoke' }).click();
    await expect(page.getByTestId('confirm-modal')).toContainText(name);
    await shot(page, '03-revoke-confirm');
    await page.getByTestId('modal-cancel').click();
    await expect(row).toBeVisible();
    expect((await request.get(`${apiUrl}/auth/me`, bearer)).status()).toBe(200);

    await row.getByRole('button', { name: 'Revoke' }).click();
    await page.getByTestId('modal-confirm').click();
    await expect(row).toHaveCount(0);
    expect((await request.get(`${apiUrl}/auth/me`, bearer)).status()).toBe(401);
    await shot(page, '04-revoked');
  });

  test("another user sees none of these tokens and cannot revoke one", async ({ page, browser, request }) => {
    test.skip(!otherUser || !otherPassword, 'set STUDIO_LIVE_OTHER_USER and STUDIO_LIVE_OTHER_PASSWORD');
    await signIn(page, username as string, password as string);
    await page.goto('/account/tokens');
    const name = `live-owner-${suffix}`;
    const secret = await createToken(page, name, 'write', 'never');
    await page.getByRole('button', { name: 'I have saved it' }).click();
    const ownerList = await page.request.get(`${apiUrl}/auth/tokens`);
    const id = ((await ownerList.json()) as Array<{ id: string; name: string }>).find((t) => t.name === name)?.id;
    expect(id).toBeTruthy();

    const other = await newSignedInPage(browser, otherUser as string, otherPassword as string);
    await other.goto('/account/tokens');
    await expect(other.getByRole('heading', { name: 'Access tokens' })).toBeVisible();
    await expect(other.getByRole('row').filter({ hasText: name })).toHaveCount(0);
    await shot(other, '05-other-user-sees-nothing');
    const foreign = await other.request.delete(`${apiUrl}/auth/tokens/${id}`, { headers: { Origin: new URL(studioUrl as string).origin } });
    expect(foreign.status(), await foreign.text()).toBe(403);
    expect((await request.get(`${apiUrl}/auth/me`, { headers: { Authorization: `Bearer ${secret}` } })).status()).toBe(200);
    await other.context().close();
  });
});
