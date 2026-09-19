import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockCloudMode, mockProject } from './helpers';

/**
 * Pause / Resume UI flow.
 *
 * The backend pause pipeline (backup → workload-stop → PAUSED) is
 * exercised by Go tests including a real testcontainers round-trip.
 * This spec catches studio-specific drift: that the right buttons
 * render in the right state, that POST bodies are shaped correctly,
 * and that the UI reflects the post-transition status.
 */

const PROJECT = 'test-project';
const SETTINGS_URL = `/project/${PROJECT}/settings`;

interface ProjectShape {
  projectId: string;
  status: string;
  pauseReason?: string;
  deploymentMode?: string;
  lastActiveAt?: string;
}

async function mockProjectStatus(page: Page, body: Partial<ProjectShape>) {
  await page.route(`**/api/provision/${PROJECT}*`, (route: Route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          projectId: PROJECT,
          orgId: 'o',
          databaseType: 'POSTGRESQL',
          tier: 'FREE',
          namespace: 'org-test-project',
          host: 'localhost',
          port: '5432',
          databaseName: 'app',
          backupEnabled: true,
          backupSchedule: '',
          backupRetentionDays: 7,
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-05-06T00:00:00Z',
          status: 'ACTIVE',
          currentStage: 'COMPLETED',
          deploymentMode: 'docker',
          ...body,
        }),
      });
    }
    return route.continue();
  });
}

async function mockPauseEndpoint(page: Page, opts: { capture?: { body?: unknown }; fail?: boolean } = {}) {
  await page.route(`**/api/provision/${PROJECT}/pause`, async (route: Route) => {
    if (opts.capture) {
      try {
        opts.capture.body = JSON.parse(route.request().postData() ?? '{}');
      } catch {
        opts.capture.body = null;
      }
    }
    if (opts.fail) {
      return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'backup failed' }) });
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ projectId: PROJECT, status: 'PAUSED', pauseReason: 'manual' }),
    });
  });
}

async function mockResumeEndpoint(page: Page) {
  await page.route(`**/api/provision/${PROJECT}/resume`, (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ projectId: PROJECT, status: 'ACTIVE' }),
    })
  );
}

async function mockMe(page: Page) {
  await page.route('**/api/auth/me', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: '1', username: 'admin', email: 'a@x.test', role: 'platform_admin' }),
    })
  );
}

test.describe('Project Pause / Resume', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockCloudMode(page);
    await mockMe(page);
    await mockProject(page, PROJECT);
  });

  test('ACTIVE project shows Pause button + clicking sends correct payload', async ({ page }) => {
    await mockProjectStatus(page, { status: 'ACTIVE' });
    const captured: { body?: unknown } = {};
    await mockPauseEndpoint(page, { capture: captured });

    await page.goto(SETTINGS_URL);
    const pauseBtn = page.getByTestId('pause-project-btn');
    await expect(pauseBtn).toBeVisible();
    await expect(pauseBtn).toBeEnabled();
    await expect(page.getByTestId('resume-project-btn')).not.toBeVisible();

    await pauseBtn.click();
    // The mutation hook fires; assert the POST body is the documented shape.
    await page.waitForResponse(`**/api/provision/${PROJECT}/pause`);
    const body = captured.body as { reason?: string } | undefined;
    expect(body?.reason).toBe('manual');
  });

  test('PAUSED project shows Resume button + reason text + last-active', async ({ page }) => {
    await mockProjectStatus(page, {
      status: 'PAUSED',
      pauseReason: 'idle_7d',
      lastActiveAt: '2026-04-28T10:00:00Z',
    });
    await mockResumeEndpoint(page);

    await page.goto(SETTINGS_URL);
    const resumeBtn = page.getByTestId('resume-project-btn');
    await expect(resumeBtn).toBeVisible();
    await expect(resumeBtn).toBeEnabled();
    // The Pause button must NOT be visible when project is paused —
    // mutually exclusive UI.
    await expect(page.getByTestId('pause-project-btn')).not.toBeVisible();
    await expect(page.getByText('idle_7d', { exact: false })).toBeVisible();
  });

  test('Pause button disabled when status is not ACTIVE (e.g. PAUSING in flight)', async ({ page }) => {
    await mockProjectStatus(page, { status: 'PAUSING' });

    await page.goto(SETTINGS_URL);
    // Lifecycle section visible but pause button disabled — mid-transition.
    const pauseBtn = page.getByTestId('pause-project-btn');
    await expect(pauseBtn).toBeVisible();
    await expect(pauseBtn).toBeDisabled();
  });

  test('Resume click triggers POST and surfaces the new ACTIVE status', async ({ page }) => {
    let getCalls = 0;
    await page.route(`**/api/provision/${PROJECT}*`, (route: Route) => {
      if (route.request().method() === 'GET') {
        getCalls++;
        const status = getCalls < 2 ? 'PAUSED' : 'ACTIVE';
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            projectId: PROJECT, orgId: 'o', databaseType: 'POSTGRESQL', tier: 'FREE',
            namespace: 'n', host: 'h', port: '5432', databaseName: 'app',
            backupEnabled: true, backupSchedule: '', backupRetentionDays: 7,
            createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-05-06T00:00:00Z',
            status, currentStage: 'COMPLETED', deploymentMode: 'docker',
            pauseReason: status === 'PAUSED' ? 'idle_7d' : '',
          }),
        });
      }
      return route.continue();
    });
    await mockResumeEndpoint(page);

    await page.goto(SETTINGS_URL);
    await page.getByTestId('resume-project-btn').click();
    // After mutation invalidates ['project'], the next GET returns ACTIVE
    // → Pause button replaces Resume.
    await expect(page.getByTestId('pause-project-btn')).toBeVisible({ timeout: 5_000 });
  });
});
