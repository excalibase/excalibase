import { test, expect, type Page } from '@playwright/test';

// A brand-new user's first hour, against a real stack, never mocks:
//   STUDIO_LIVE_URL=http://studio.example.test \
//   STUDIO_LIVE_MAILBOX_URL=<GET returning the captured outgoing mail as text> \
//   npx playwright test e2e/live/journey.live.spec.ts
// It signs up a fresh account, so the stack must send mail somewhere the
// mailbox URL can read. It creates one project (the account's free slot) and
// schedules it for deletion at the end, whatever happens.
// STUDIO_LIVE_APP_IMAGE overrides the public image deployed (default below).

const studioUrl = process.env.STUDIO_LIVE_URL;
const mailboxUrl = process.env.STUDIO_LIVE_MAILBOX_URL;
const appImage = process.env.STUDIO_LIVE_APP_IMAGE ?? 'nginxinc/nginx-unprivileged:1.27-alpine';

const suffix = Date.now().toString(36);
const username = `journey${suffix}`;
const email = `journey-${suffix}@example.test`;
const password = `Journey-${suffix}-9x`;
const PROJECT_READY_MS = 10 * 60_000;

let projectId = '';

// The verify link sent to this run's address: the newest mail that names it.
async function verifyLink(page: Page): Promise<string> {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const mail = await (await page.request.get(mailboxUrl as string)).text();
    const afterAddress = mail.slice(mail.lastIndexOf(email));
    const match = afterAddress.match(/\/verify-email\?token=[A-Za-z0-9_%-]+/);
    if (match) return match[0];
    await page.waitForTimeout(2000);
  }
  throw new Error(`no verification mail for ${email}`);
}

// Through the page, so the call carries Studio's session and origin as the app's own do.
async function studioFetch(page: Page, path: string, method = 'GET', body?: unknown): Promise<{ status: number; data: unknown }> {
  return page.evaluate(async ([target, verb, payload]) => {
    const headers: Record<string, string> = { Prefer: 'respond-async' };
    if (payload !== undefined) headers['Content-Type'] = 'application/json';
    const response = await fetch(target, {
      method: verb,
      credentials: 'include',
      headers,
      body: payload === undefined ? undefined : JSON.stringify(payload),
    });
    const data = response.headers.get('content-type')?.includes('json') ? await response.json() : null;
    return { status: response.status, data };
  }, [path, method, body] as const);
}

async function projectStatus(page: Page): Promise<string> {
  const { data } = await studioFetch(page, `/api/provision/${projectId}`);
  return (data as { status?: string } | null)?.status ?? '';
}

test.describe.serial('a new user, from sign-up to a deployed app', () => {
  test.skip(!studioUrl || !mailboxUrl, 'set STUDIO_LIVE_URL and STUDIO_LIVE_MAILBOX_URL');
  test.setTimeout(PROJECT_READY_MS + 5 * 60_000);

  let page: Page;
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext();
    // A local stack on plain http: the session cookie is Secure, so the browser
    // would drop it. STUDIO_LIVE_PLAIN_HTTP_VIA (e.g. http://127.0.0.1:8080) is
    // where the edge listens; auth calls go there and the cookie keeps working.
    const via = process.env.STUDIO_LIVE_PLAIN_HTTP_VIA;
    if (via) {
      await context.route('**/api/auth/**', async (route) => {
        const original = new URL(route.request().url());
        const response = await route.fetch({
          url: `${via}${original.pathname}${original.search}`,
          headers: { ...route.request().headers(), host: original.host },
        });
        const headers = { ...response.headers() };
        if (headers['set-cookie']) headers['set-cookie'] = headers['set-cookie'].replace(/;\s*Secure/gi, '');
        await route.fulfill({ response, headers });
      });
    }
    page = await context.newPage();
  });

  // New projects are protected from deletion, so protection goes off first;
  // a refusal fails the run rather than leaving the project behind unnoticed.
  test.afterAll(async () => {
    if (projectId) {
      const unprotect = await studioFetch(page, `/api/provision/${projectId}/deletion-protection`, 'PATCH', { enabled: false });
      const removal = await studioFetch(page, `/api/provision/${projectId}`, 'DELETE');
      expect(unprotect.status, JSON.stringify(unprotect.data)).toBeLessThan(300);
      expect(removal.status, JSON.stringify(removal.data)).toBeLessThan(300);
    }
    await page.context().close();
  });

  test('signs up and is asked to check their e-mail', async () => {
    await page.goto('/register');
    await page.fill('#username', username);
    await page.fill('#email', email);
    const passwords = page.locator('input[type=password]');
    await passwords.nth(0).fill(password);
    await passwords.nth(1).fill(password);
    await page.getByRole('button', { name: /create account/i }).click();
    await expect(page.getByTestId('check-email')).toBeVisible();
  });

  test('verifies the address from the e-mail and signs in', async () => {
    await page.goto(await verifyLink(page));
    await expect(page.getByText(/verified/i).first()).toBeVisible();
    await page.goto('/login');
    await page.locator('input').first().fill(email);
    await page.locator('input[type=password]').fill(password);
    await page.getByRole('button', { name: /^sign in$/i }).click();
    await expect(page).not.toHaveURL(/\/login/);
  });

  test('creates a first PostgreSQL 17 project and waits for it', async () => {
    await page.goto('/provision');
    await page.getByText('PostgreSQL 17').click();
    await page.fill('#provision-project-name', `journey ${suffix}`);
    await page.getByTestId('provision-submit').click();
    await expect(page).toHaveURL(/\/project\/[^/]+$/, { timeout: 30_000 });
    projectId = new URL(page.url()).pathname.split('/')[2];
    await expect.poll(() => projectStatus(page), { timeout: PROJECT_READY_MS, intervals: [5000] }).toBe('ACTIVE');
  });

  test('creates a table', async () => {
    await page.goto(`/project/${projectId}/database/tables`);
    await page.getByTestId('new-table-btn').click();
    await page.getByTestId('table-name-input').fill('todos');
    await page.getByRole('button', { name: /add column/i }).first().click();
    await page.getByPlaceholder('e.g. email').last().fill('title');
    await page.getByPlaceholder('e.g. text').last().fill('text');
    await page.getByTestId('create-table-submit').click();
    await expect(page.getByTestId('table-item-todos')).toBeVisible({ timeout: 20_000 });
  });

  test('runs a query in the SQL editor', async () => {
    await page.goto(`/project/${projectId}/sql`);
    const editor = page.locator('.cm-content').first();
    await editor.click();
    await page.keyboard.press('Control+A');
    await page.keyboard.type("insert into todos (title) values ('ship it') returning title");
    await page.getByRole('button', { name: /run/i }).first().click();
    await expect(page.getByText('ship it').last()).toBeVisible({ timeout: 20_000 });
  });

  test('creates an API key and sees it once', async () => {
    await page.goto(`/project/${projectId}/api-keys`);
    await page.locator('input').first().fill('web');
    await page.getByRole('button', { name: /generate key/i }).first().click();
    await expect(page.locator('code, pre').filter({ hasText: /\S{20,}/ }).first()).toBeVisible({ timeout: 20_000 });
  });

  test('deploys a container from a public image', async () => {
    await page.goto(`/project/${projectId}/containers/new`);
    await page.getByTestId('app-image').fill(appImage);
    await page.getByTestId('app-name').fill('web');
    await page.getByTestId('app-port').fill('8080');
    await page.getByTestId('app-submit').click();
    await expect(page).toHaveURL(/\/containers\/[0-9a-f-]{36}$/, { timeout: 30_000 });
    await page.getByRole('button', { name: /^deploy$/i }).first().click();
    await expect(page.getByText('Running').first()).toBeVisible({ timeout: 5 * 60_000 });
  });
});
