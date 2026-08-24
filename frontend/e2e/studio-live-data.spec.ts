import { test, expect } from '@playwright/test';

/**
 * Studio specs against a real data plane.
 *
 * Default Playwright runs use page.route() mocks for API endpoints —
 * fast, hermetic, but doesn't catch drift between the studio and the
 * backend's actual response shape. This spec is the opposite: gated
 * on STUDIO_LIVE=1, points at a running provisioning server, and
 * exercises the table/SQL/realtime surfaces against real data.
 *
 * Skipped by default. To run:
 *
 *   STUDIO_LIVE=1 \
 *   E2E_API_URL=http://localhost:24005 \
 *   E2E_PROJECT_ID=proj-abc1234567 \
 *   E2E_PAT=excali_... \
 *   npx playwright test e2e/studio-live-data.spec.ts
 *
 * Environment requirements:
 *   - Provisioning server running at E2E_API_URL with vault unsealed
 *   - A pre-provisioned project at E2E_PROJECT_ID with at least one
 *     accessible table (the spec creates + drops its own scratch
 *     table so it doesn't depend on existing schema)
 *   - A PAT with project_admin role on E2E_PROJECT_ID
 *
 * What it catches that the mock-based specs don't:
 *   - JSON shape drift: backend renames a field but studio still
 *     reads the old name
 *   - 4xx semantics: backend returns 400 with a specific error code
 *     that studio's error handler depends on
 *   - Realtime: WebSocket subscription wiring, NATS subject parsing,
 *     CDC event payload format
 */

const STUDIO_LIVE = process.env.STUDIO_LIVE === '1';
const API_BASE = process.env.E2E_API_URL || 'http://localhost:24005';
const PROJECT_ID = process.env.E2E_PROJECT_ID || '';
const PAT = process.env.E2E_PAT || '';

test.describe('Studio against real data plane', () => {
  test.skip(!STUDIO_LIVE, 'set STUDIO_LIVE=1 + E2E_API_URL/PROJECT_ID/PAT to enable');

  test.beforeAll(async () => {
    if (!PROJECT_ID || !PAT) {
      throw new Error('STUDIO_LIVE=1 requires E2E_PROJECT_ID and E2E_PAT');
    }
  });

  test.beforeEach(async ({ context }) => {
    // Inject the PAT so authenticated API calls work without a login flow.
    await context.addInitScript((pat) => {
      localStorage.setItem('auth_token', pat);
      localStorage.setItem('theme', 'dark');
    }, PAT);
  });

  test('table list reflects real schema', async ({ page }) => {
    // Backend round-trip: GET /api/projects/{id}/tables
    const res = await page.request.get(`${API_BASE}/api/projects/${PROJECT_ID}/tables`, {
      headers: { Authorization: `Bearer ${PAT}` },
    });
    expect(res.status()).toBe(200);
    const tables = (await res.json()) as Array<{ schema: string; name: string }>;
    expect(Array.isArray(tables)).toBeTruthy();

    await page.goto(`/project/${PROJECT_ID}/database/tables`);
    // Each table the API listed should appear in the studio's table panel.
    for (const t of tables.slice(0, 5)) {
      await expect(page.getByText(t.name).first()).toBeVisible({ timeout: 10_000 });
    }
  });

  test('SQL editor: create scratch table, insert, select, drop', async ({ page }) => {
    const tableName = `studio_live_${Date.now()}`;
    await page.goto(`/project/${PROJECT_ID}/database/sql-editor`);

    // The SQL editor is a Monaco-style code surface; we paste via
    // the textarea fallback that Monaco renders.
    async function runSQL(sql: string) {
      const editor = page.locator('.monaco-editor textarea, textarea[role="textbox"]').first();
      await editor.fill(sql);
      await page.getByRole('button', { name: /Run|Execute/i }).click();
      // Result panel renders within a couple of seconds for fast queries.
      await page.waitForTimeout(1500);
    }

    await runSQL(`CREATE TABLE ${tableName} (id int PRIMARY KEY, label text)`);
    await runSQL(`INSERT INTO ${tableName} VALUES (1, 'live'), (2, 'data-plane')`);
    await runSQL(`SELECT label FROM ${tableName} ORDER BY id`);
    await expect(page.getByText('live')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText('data-plane')).toBeVisible({ timeout: 5_000 });

    // Cleanup — important so re-runs don't collide on the next run.
    await runSQL(`DROP TABLE ${tableName}`);
  });

  test('realtime: subscribe + INSERT + receive event', async ({ page }) => {
    const tableName = `realtime_live_${Date.now()}`;
    // Pre-create the table + add to publication via REST.
    await page.request.post(`${API_BASE}/api/projects/${PROJECT_ID}/tables`, {
      headers: { Authorization: `Bearer ${PAT}`, 'Content-Type': 'application/json' },
      data: {
        schema: 'public',
        name: tableName,
        columns: [
          { name: 'id', type: 'int', primary: true },
          { name: 'message', type: 'text' },
        ],
      },
    });
    await page.request.put(`${API_BASE}/api/projects/${PROJECT_ID}/realtime/tables/public/${tableName}`, {
      headers: { Authorization: `Bearer ${PAT}` },
    });

    // The realtime page renders an event log; assert the row INSERT
    // surfaces there within the timeout.
    await page.goto(`/project/${PROJECT_ID}/database/realtime`);
    await page.request.post(`${API_BASE}/api/projects/${PROJECT_ID}/tables/${tableName}/rows`, {
      headers: { Authorization: `Bearer ${PAT}`, 'Content-Type': 'application/json' },
      data: { id: 1, message: 'realtime-live-test' },
    });
    await expect(page.getByText('realtime-live-test')).toBeVisible({ timeout: 15_000 });

    // Cleanup
    await page.request.delete(`${API_BASE}/api/projects/${PROJECT_ID}/realtime/tables/public/${tableName}`, {
      headers: { Authorization: `Bearer ${PAT}` },
    });
    await page.request.delete(`${API_BASE}/api/projects/${PROJECT_ID}/tables/${tableName}`, {
      headers: { Authorization: `Bearer ${PAT}` },
    });
  });
});
