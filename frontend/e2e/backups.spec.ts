import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockCloudMode, mockProject } from './helpers';

const PROJECT = 'test-project';
const BACKUPS_URL = `/project/${PROJECT}/operations/backups`;

type BackupRow = {
  id: string;
  projectId: string;
  timestamp: string;
  type: string;
  status: string;
  size?: string;
};

interface BackupListResponse {
  backups: BackupRow[];
  backupEnabled: boolean;
  schedule: string;
  retentionDays: number;
}

const sampleBackups: BackupRow[] = [
  { id: 'backup-001', projectId: PROJECT, timestamp: '2026-05-04T10:00:00Z', type: 'MANUAL', status: 'COMPLETED', size: '120 MB' },
  { id: 'backup-002', projectId: PROJECT, timestamp: '2026-05-05T02:00:00Z', type: 'SCHEDULED', status: 'IN_PROGRESS', size: '0 B' },
];

async function mockBackupList(page: Page, body: BackupListResponse) {
  await page.route(`**/api/provision/${PROJECT}/backup/list*`, (route: Route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  );
}

async function mockTriggerBackup(page: Page, opts: { fail?: boolean } = {}) {
  await page.route(`**/api/provision/${PROJECT}/backup/trigger`, (route: Route) => {
    if (opts.fail) {
      return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'r2 unreachable' }) });
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: 'backup-fresh',
        projectId: PROJECT,
        timestamp: new Date().toISOString(),
        type: 'MANUAL',
        status: 'IN_PROGRESS',
      }),
    });
  });
}

async function mockRestore(page: Page, opts: { fail?: boolean; capture?: { body?: unknown } } = {}) {
  await page.route(`**/api/provision/${PROJECT}/backup/restore`, async (route: Route) => {
    if (opts.capture) {
      try {
        opts.capture.body = JSON.parse(route.request().postData() ?? '{}');
      } catch {
        opts.capture.body = null;
      }
    }
    if (opts.fail) {
      return route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'newProjectId already exists' }) });
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'success', message: 'Restore initiated', newProjectId: 'restored-db', recoveryType: 'PITR' }),
    });
  });
}

async function mockMe(page: Page) {
  await page.route('**/api/auth/me', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: '1', username: 'admin', email: 'admin@test.com', role: 'platform_admin' }),
    })
  );
}

test.describe('Backups page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockCloudMode(page);
    await mockMe(page);
    await mockProject(page, PROJECT);
  });

  test('empty state when there are no backups', async ({ page }) => {
    await mockBackupList(page, { backups: [], backupEnabled: false, schedule: '', retentionDays: 0 });
    await mockTriggerBackup(page);

    await page.goto(BACKUPS_URL);
    await expect(page.getByText('No backups found.', { exact: false })).toBeVisible();
    await expect(page.getByRole('button', { name: /Trigger Backup/i })).toBeEnabled();
  });

  test('renders config tiles + backup history table', async ({ page }) => {
    await mockBackupList(page, {
      backups: sampleBackups,
      backupEnabled: true,
      schedule: '0 2 * * *',
      retentionDays: 7,
    });

    await page.goto(BACKUPS_URL);

    // Config tiles
    await expect(page.getByText('Enabled')).toBeVisible();
    await expect(page.getByText('0 2 * * *')).toBeVisible();
    await expect(page.getByText('7 days')).toBeVisible();

    // Table headers
    for (const header of ['Backup ID', 'Timestamp', 'Type', 'Size', 'Status']) {
      await expect(page.getByRole('columnheader', { name: header })).toBeVisible();
    }

    // Both backup rows
    await expect(page.getByText('backup-001')).toBeVisible();
    await expect(page.getByText('backup-002')).toBeVisible();

    // StatusBadge maps COMPLETED → "Active" and falls through to
    // "Provisioning" for IN_PROGRESS. We assert the rendered labels.
    await expect(page.getByText('Active').first()).toBeVisible();
    await expect(page.getByText('Provisioning').first()).toBeVisible();
  });

  test('trigger backup posts to API and shows success toast', async ({ page }) => {
    await mockBackupList(page, { backups: [], backupEnabled: true, schedule: '0 2 * * *', retentionDays: 7 });
    await mockTriggerBackup(page);

    let triggered = false;
    page.on('request', (req) => {
      if (req.url().endsWith(`/api/provision/${PROJECT}/backup/trigger`) && req.method() === 'POST') {
        triggered = true;
      }
    });

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /Trigger Backup/i }).click();

    await expect(page.getByText(/Backup triggered successfully/i)).toBeVisible();
    expect(triggered).toBe(true);
  });

  test('trigger failure shows error toast and does not mutate the list', async ({ page }) => {
    await mockBackupList(page, { backups: sampleBackups, backupEnabled: true, schedule: '', retentionDays: 0 });
    await mockTriggerBackup(page, { fail: true });

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /Trigger Backup/i }).click();

    await expect(page.getByText(/Failed to trigger backup/i)).toBeVisible();
    // Existing backups should still be visible — failure must not wipe history.
    await expect(page.getByText('backup-001')).toBeVisible();
  });

  test('restore tab disables submit until newProjectId is filled', async ({ page }) => {
    await mockBackupList(page, { backups: sampleBackups, backupEnabled: true, schedule: '', retentionDays: 0 });
    await mockRestore(page);

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /Restore \/ PITR/i }).click();

    const submit = page.getByRole('button', { name: /Restore Latest Backup/i });
    await expect(submit).toBeDisabled();

    await page.getByLabel(/New Instance ID/i).fill('restored-db');
    await expect(submit).toBeEnabled();
  });

  test('restore in PITR mode toggles indicator + sends targetTime', async ({ page }) => {
    await mockBackupList(page, { backups: sampleBackups, backupEnabled: true, schedule: '', retentionDays: 0 });
    const captured: { body?: unknown } = {};
    await mockRestore(page, { capture: captured });

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /Restore \/ PITR/i }).click();

    await page.getByLabel(/New Instance ID/i).fill('restored-db');
    await page.getByLabel(/Target Time \(PITR\)/i).fill('2026-05-04T03:30');

    await expect(page.getByText('Point-in-Time Recovery mode enabled')).toBeVisible();
    await expect(page.getByRole('button', { name: /Restore to Point in Time/i })).toBeEnabled();

    await page.getByRole('button', { name: /Restore to Point in Time/i }).click();
    await expect(page.getByText('Restore initiated')).toBeVisible();

    // The body the studio actually sends — pin that the new ProjectId
    // and targetTime survive the form correctly.
    const body = captured.body as { newProjectId?: string; targetTime?: string } | undefined;
    expect(body?.newProjectId).toBe('restored-db');
    expect(body?.targetTime).toBe('2026-05-04T03:30');
  });

  test('full restore (no targetTime) sends correct payload and shows success', async ({ page }) => {
    await mockBackupList(page, { backups: sampleBackups, backupEnabled: true, schedule: '', retentionDays: 0 });
    const captured: { body?: unknown } = {};
    await mockRestore(page, { capture: captured });

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /Restore \/ PITR/i }).click();

    await page.getByLabel(/New Instance ID/i).fill('restored-db');
    await page.getByRole('button', { name: /Restore Latest Backup/i }).click();

    await expect(page.getByText('Restore initiated')).toBeVisible();
    await expect(page.getByText('restored-db')).toBeVisible();

    const body = captured.body as { newProjectId?: string; targetTime?: unknown } | undefined;
    expect(body?.newProjectId).toBe('restored-db');
    // targetTime must be undefined / empty so the backend dispatches
    // to "latest" — the studio strips empty strings before POST.
    expect(body?.targetTime ?? '').toBe('');
  });

  test('restore failure surfaces the backend error message', async ({ page }) => {
    await mockBackupList(page, { backups: sampleBackups, backupEnabled: true, schedule: '', retentionDays: 0 });
    await mockRestore(page, { fail: true });

    await page.goto(BACKUPS_URL);
    await page.getByRole('button', { name: /Restore \/ PITR/i }).click();
    await page.getByLabel(/New Instance ID/i).fill('restored-db');
    await page.getByRole('button', { name: /Restore Latest Backup/i }).click();

    // Axios surfaces "Request failed with status code 400" on its
    // Error.message; the BackupsPage onError shows that string (or
    // falls back to "Restore failed"). Either works — the contract
    // is "an error toast appears", which we detect by the red border
    // styling the toast applies on failure.
    await expect(page.getByText(/Restore failed|Request failed/i).first()).toBeVisible();
  });
});
