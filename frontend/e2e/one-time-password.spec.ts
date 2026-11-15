import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { loginAs, mockProjectRole, mockSchemaEndpoints } from './helpers';

// EXC-759: the database password is shown once, never readable again.

const FIRST_RENDER = 20_000;
const PROJECT_ID = 'test-project';
const SHOWN_ONCE = 'Shown-Once-Pass-77xQ9';
const RESET_PASSWORD = 'Reset-Once-Pass-31zLp';

const CREDENTIALS = {
  projectId: PROJECT_ID, host: 'test-project-postgres-rw.org-1.svc.cluster.local', port: 5432, databaseName: 'app',
  username: 'excalibase_app', sslMode: 'require', connectionUrl: 'postgresql://excalibase_app:<password>@h:5432/app',
};

const ENDPOINT = {
  projectId: PROJECT_ID, publicOffered: true, publicEnabled: true, available: true, host: 'test-project.db.example.test',
  port: 30100, requireTls: true, database: 'app', username: 'excalibase_app',
  connectionStrings: { requireTls: '', allowPlaintext: '' }, caCertificate: '',
  canChange: true,
};

async function json(page: Page, pattern: string, body: unknown, status = 200) {
  await page.route(pattern, (route) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

// Records every credentials answer so the test can prove none carried a password.
async function mockControlPlane(page: Page) {
  await loginAs(page);
  await mockSchemaEndpoints(page, PROJECT_ID);
  await json(page, '**/api/config', { deploymentMode: 'cloud', features: { onetimecredentials: true } });
  await json(page, '**/api/postgres/catalog', { documentDbRef: 'v', majors: [{ major: '17', available: true, documentDb: true }] });
  await json(page, '**/api/tiers', []);
  await mockProjectRole(page, 'admin', PROJECT_ID);
  await json(page, `**/api/projects/${PROJECT_ID}/db-endpoint`, ENDPOINT);
  const credentialAnswers: string[] = [];
  await page.route(`**/api/provision/${PROJECT_ID}/credentials`, (route) => {
    const body = JSON.stringify(CREDENTIALS);
    credentialAnswers.push(body);
    return route.fulfill({ status: 200, contentType: 'application/json', body });
  });
  return credentialAnswers;
}

test.describe('Database password, shown once', () => {
  test('create: panel, CSV, saved, then the password is nowhere', async ({ page }) => {
    const credentialAnswers = await mockControlPlane(page);
    let requested: Record<string, unknown> | undefined;
    await page.route('**/api/provision', (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      requested = route.request().postDataJSON();
      return route.fulfill({
        status: 202, contentType: 'application/json',
        body: JSON.stringify({ projectId: PROJECT_ID, status: 'PROVISIONING', password: SHOWN_ONCE }),
      });
    });

    await page.goto('/provision');
    await page.getByLabel('Project Name').fill('my-db');
    await page.getByTestId('pg-version-17').click({ timeout: FIRST_RENDER });
    await page.getByTestId('provision-submit').click();

    await expect(page.getByTestId('otp-password')).toHaveText(SHOWN_ONCE, { timeout: FIRST_RENDER });
    expect(requested).not.toHaveProperty('password');
    await expect(page.getByText('Copy it now. It will not be shown again.')).toBeVisible();

    const download = page.waitForEvent('download');
    await page.getByRole('link', { name: 'Download CSV' }).click();
    const file = await download;
    expect(file.suggestedFilename()).toBe(`${PROJECT_ID}-database-credentials.csv`);
    const csv = readFileSync(await file.path(), 'utf8');
    expect(csv.startsWith('host,port,database,user,password,sslmode\n')).toBe(true);
    expect(csv).toContain(`"${SHOWN_ONCE}"`);

    await page.getByRole('button', { name: "I've saved it" }).click();
    await expect(page).toHaveURL(new RegExp(`/project/${PROJECT_ID}$`), { timeout: FIRST_RENDER });

    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('conn-postgres-public')).toContainText(':<password>@', { timeout: FIRST_RENDER });
    await page.reload();
    await expect(page.getByTestId('conn-postgres-public')).toContainText(':<password>@', { timeout: FIRST_RENDER });
    await expect(page.locator('body')).not.toContainText(SHOWN_ONCE);
    expect(await page.content()).not.toContain(SHOWN_ONCE);
    expect(credentialAnswers.length).toBeGreaterThan(0);
    for (const answer of credentialAnswers) expect(answer).not.toContain(SHOWN_ONCE);
    await expect(page.locator('body')).not.toContainText('svc.cluster.local');
  });

  test('create: a short chosen password is refused, a good one is sent', async ({ page }) => {
    await mockControlPlane(page);
    const bodies: Record<string, unknown>[] = [];
    await page.route('**/api/provision', (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      bodies.push(route.request().postDataJSON());
      return route.fulfill({
        status: 202, contentType: 'application/json',
        body: JSON.stringify({ projectId: PROJECT_ID, status: 'PROVISIONING', password: 'my chosen passphrase here' }),
      });
    });
    await page.goto('/provision');
    await page.getByLabel('Project Name').fill('my-db');
    await page.getByTestId('pg-version-17').click({ timeout: FIRST_RENDER });
    await page.getByLabel(/database password/i).fill('short');
    await page.getByTestId('provision-submit').click();
    await expect(page.getByTestId('password-error')).toContainText('at least 16');
    expect(bodies).toHaveLength(0);
    await page.getByLabel(/database password/i).fill('my chosen passphrase here');
    await page.getByTestId('provision-submit').click();
    await expect(page.getByTestId('otp-password')).toBeVisible({ timeout: FIRST_RENDER });
    expect(bodies[0]).toMatchObject({ password: 'my chosen passphrase here' });
  });

  test('reset: a generated password is shown once and gone after saving and a reload', async ({ page }) => {
    const credentialAnswers = await mockControlPlane(page);
    let rotateBody: string | null = null;
    await page.route(`**/api/provision/${PROJECT_ID}/credentials/rotate`, (route) => {
      rotateBody = route.request().postData();
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ ...CREDENTIALS, password: RESET_PASSWORD }),
      });
    });
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('conn-password-note')).toContainText('Reset password to get new credentials', { timeout: FIRST_RENDER });
    await page.getByRole('button', { name: 'Reset password' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Reset', exact: true }).click();

    await expect(page.getByTestId('otp-password')).toHaveText(RESET_PASSWORD, { timeout: FIRST_RENDER });
    expect(rotateBody).toBeNull();
    await page.getByRole('button', { name: "I've saved it" }).click();
    await expect(page.getByTestId('otp-password')).toHaveCount(0);
    await expect(page.locator('body')).not.toContainText(RESET_PASSWORD);

    await page.reload();
    await expect(page.getByTestId('conn-postgres-public')).toContainText(':<password>@', { timeout: FIRST_RENDER });
    expect(await page.content()).not.toContain(RESET_PASSWORD);
    for (const answer of credentialAnswers) expect(answer).not.toContain(RESET_PASSWORD);
  });
});
