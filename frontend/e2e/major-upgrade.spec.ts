import { test, expect, Page } from '@playwright/test';
import { loginAs, mockInstances, mockSchemaEndpoints } from './helpers';

const PROJECT_ID = 'test-project';
// The dev server compiles lazy routes on first request.
const FIRST_RENDER = 20_000;
const UPGRADE_URL = `**/api/provision/${PROJECT_ID}/major-upgrade`;

const RUN = {
  id: 'mu-1', projectId: PROJECT_ID, engine: 'POSTGRESQL', fromVersion: '16', toVersion: '17',
  status: 'RUNNING', step: 'UPGRADE_CHECKING', safetyBackupId: 'backup-1', converted: false, warnings: [],
  startedAt: '2026-10-29T10:00:00Z',
};

function upgradeInfo(overrides: Record<string, unknown> = {}) {
  return {
    engine: 'POSTGRESQL', deploymentMode: 'k8s', currentVersion: '16', targets: ['17'], supported: true,
    reason: '', blockers: [], downtime: 'The database is offline for a few minutes.', latest: null,
    ...overrides,
  };
}

async function json(page: Page, pattern: string, body: unknown, status = 200) {
  await page.route(pattern, (route) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

async function mockProject(page: Page, majorupgrade: boolean) {
  await json(page, '**/api/config', { deploymentMode: 'cloud', features: { majorupgrade } });
  await mockInstances(page, PROJECT_ID);
  await mockSchemaEndpoints(page, PROJECT_ID);
  await json(page, '**/api/orgs', [{ id: 'org-1', name: 'Org One', slug: 'org-one', tier: 'FREE', ownerId: '1', role: 'admin' }]);
  await json(page, `**/api/provision/${PROJECT_ID}`, {
    projectId: PROJECT_ID, projectName: 'shop', orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE',
    namespace: 'org-1-test-project', status: 'ACTIVE', currentStage: 'COMPLETED', host: 'localhost', port: 5432,
    databaseName: 'app', postgresVersion: '16', backupEnabled: false,
    createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
  });
}

test.describe('Major version upgrade', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
  });

  test('is not shown while the flag is off', async ({ page }) => {
    await mockProject(page, false);
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('minor-upgrade-card')).toBeVisible({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('major-upgrade-card')).toHaveCount(0);
  });

  test('blockers keep the upgrade from starting', async ({ page }) => {
    await mockProject(page, true);
    await json(page, UPGRADE_URL, upgradeInfo({ blockers: ['the extension "foo" is not offered on newer majors'] }));
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('major-upgrade-blockers')).toContainText('"foo"', { timeout: FIRST_RENDER });
    await expect(page.getByTestId('major-upgrade-btn')).toBeDisabled();
  });

  test('an admin confirms by name, then follows the run to completion', async ({ page }) => {
    await mockProject(page, true);
    let started = false;
    let polls = 0;
    const posted: unknown[] = [];
    await page.route(UPGRADE_URL, (route) => {
      if (route.request().method() === 'POST') {
        posted.push(route.request().postDataJSON());
        started = true;
        return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ upgrade: RUN }) });
      }
      if (!started) return route.fulfill({ json: upgradeInfo() });
      polls += 1;
      const latest = polls < 2
        ? { ...RUN, step: 'UPGRADE_CONVERTING' }
        : { ...RUN, status: 'COMPLETED', step: 'UPGRADE_FINISHING', warnings: ['statistics are still being rebuilt'] };
      return route.fulfill({ json: upgradeInfo({ currentVersion: polls < 2 ? '16' : '17', targets: polls < 2 ? ['17'] : [], latest }) });
    });

    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('major-upgrade-target')).toHaveValue('17', { timeout: FIRST_RENDER });
    await expect(page.getByTestId('major-upgrade-warning')).toContainText('cannot be undone');

    await page.getByTestId('major-upgrade-btn').click();
    await page.getByTestId('confirm-input').fill('sho');
    await expect(page.getByTestId('modal-confirm')).toBeDisabled();
    await page.getByTestId('confirm-input').fill('shop');
    await page.getByTestId('modal-confirm').click();

    await expect(page.getByTestId('major-upgrade-running')).toContainText('Checking');
    expect(posted).toEqual([{ targetVersion: '17', confirm: true }]);
    await expect(page.getByTestId('major-upgrade-running')).toContainText('Converting the data', { timeout: 8_000 });
    await expect(page.getByTestId('major-upgrade-completed')).toContainText('statistics are still being rebuilt', { timeout: 8_000 });
  });
});
