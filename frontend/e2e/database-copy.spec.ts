import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockProject } from './helpers';

// EXC-667: copying a database into a new project from the Backups page.

const PROJECT = 'test-project';
const BACKUPS_URL = `/project/${PROJECT}/operations/backups`;

async function mockConfig(page: Page, dbcopy: boolean) {
  await page.route('**/api/config', (route: Route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'cloud', features: { dbcopy } }) }),
  );
}

async function mockBackups(page: Page) {
  await page.route('**/api/auth/me', (route: Route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: '1', username: 'admin', email: 'admin@test.com', role: 'platform_admin' }) }),
  );
  await page.route(`**/api/provision/${PROJECT}/backup/list*`, (route: Route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ backups: [], backupEnabled: true, schedule: '0 2 * * *', retentionDays: 7 }) }),
  );
}

test.describe('Copy database', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page, PROJECT);
    await mockBackups(page);
  });

  test('is not offered while the feature is off', async ({ page }) => {
    await mockConfig(page, false);
    await page.goto(BACKUPS_URL);
    await expect(page.getByRole('button', { name: /Backup History/i })).toBeVisible();
    await expect(page.getByRole('button', { name: /^Copy$/ })).toHaveCount(0);
  });

  test('copies the database as it is now and links the copy when it is ready', async ({ page }) => {
    await mockConfig(page, true);
    let sent: unknown;
    await page.route(`**/api/provision/${PROJECT}/backup/copy`, async (route: Route) => {
      sent = JSON.parse(route.request().postData() ?? '{}');
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: 'job-1', sourceProjectId: PROJECT, newProjectId: 'copy-1', mode: 'new_project', status: 'RUNNING', targetKind: 'now' }),
      });
    });
    let polls = 0;
    await page.route(`**/api/provision/${PROJECT}/backup/restore/job-1`, (route: Route) => {
      polls += 1;
      const job = polls === 1
        ? { status: 'RUNNING', currentStep: 'SOURCE_BACKUP' }
        : { status: 'COMPLETED' };
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: 'job-1', sourceProjectId: PROJECT, newProjectId: 'copy-1', mode: 'new_project', targetKind: 'now', ...job }),
      });
    });

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /^Copy$/ }).click();
    await expect(page.getByRole('radio', { name: /As it is now/i })).toBeChecked();
    const submit = page.getByRole('button', { name: /Copy database/i });
    await expect(submit).toBeDisabled();
    await page.getByLabel(/New project name/i).fill('staging');
    await submit.click();

    await expect(page.getByText(/Taking a backup of the database as it is now/i)).toBeVisible();
    await expect(page.getByRole('link', { name: /Open the copy/i })).toHaveAttribute('href', '/project/copy-1', { timeout: 15000 });
    expect(sent).toEqual({ name: 'staging', point: 'now' });
  });

  test('shows why a copy was refused', async ({ page }) => {
    await mockConfig(page, true);
    await page.route(`**/api/provision/${PROJECT}/backup/copy`, (route: Route) =>
      route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'copies are made from backups; turn on backups for this database first' }) }),
    );

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /^Copy$/ }).click();
    await page.getByLabel(/New project name/i).fill('staging');
    await page.getByRole('button', { name: /Copy database/i }).click();

    await expect(page.getByRole('alert')).toContainText('turn on backups for this database first');
  });
});
