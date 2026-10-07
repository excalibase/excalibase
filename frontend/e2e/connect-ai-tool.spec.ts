import { test, expect } from '@playwright/test';
import { loginAs, mockProject } from './helpers';
import type { Page } from '@playwright/test';

// MCP ships dark (EXC-554); the server says in /api/config whether it is on.
async function mockMcp(page: Page, mcp: boolean) {
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'cloud', features: { mcp, pipeline: false } }),
    }),
  );
}

test.describe('Connect your AI tool', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockMcp(page, true);
  });

  test('mints a project-bound token and shows the Gemini CLI setup once', async ({ page }) => {
    let requested: Record<string, unknown> | null = null;
    await page.route('**/api/auth/tokens', async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      requested = route.request().postDataJSON();
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ token: 'excb_e2e_secret', prefix: 'excb_e2e', name: 'Gemini CLI MCP', scopes: 'read,write', projectId: 'test-project' }),
      });
    });

    await page.goto('/project/test-project/ai-tools');
    await expect(page.getByTestId('connect-ai-tool-page')).toBeVisible();
    await page.getByRole('radio', { name: 'Gemini CLI' }).check();
    await page.getByRole('radio', { name: 'Read and write' }).check();
    await page.getByRole('button', { name: /create token and show setup/i }).click();

    const setup = page.getByTestId('mcp-setup');
    await expect(setup).toContainText('export EXCALIBASE_TOKEN=excb_e2e_secret');
    await expect(setup).toContainText('.gemini/settings.json');
    await expect(setup).toContainText('"httpUrl"');
    await expect(setup).toContainText('/mcp?project=test-project');
    await expect(setup).not.toContainText('read_only');
    expect(requested).toMatchObject({ name: 'Gemini CLI MCP', scopes: ['read', 'write'], projectId: 'test-project' });

    await setup.getByRole('button', { name: /i have saved it/i }).click();
    await expect(page.getByText('excb_e2e_secret')).toHaveCount(0);
  });

  test('the activity feed lists MCP calls and revokes the caller\'s token', async ({ page }) => {
    let revoked = '';
    await page.route('**/api/projects/test-project/ai-activity/', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ calls: revoked ? [] : [
          { id: 7, tool: 'execute_sql', status: 'ok', tokenName: 'Codex MCP', userId: 'me', at: '2026-10-06T01:00:00Z', mine: true, tokenId: 'hash-7' },
          { id: 6, tool: 'apply_migration', status: 'error', httpStatus: 403, tokenName: 'Their MCP', userId: 'other', at: '2026-10-06T00:59:00Z', mine: false },
        ] }),
      }),
    );
    await page.route('**/api/projects/test-project/ai-activity/tokens/*', (route) => {
      revoked = route.request().url().split('/').pop() ?? '';
      return route.fulfill({ status: 204 });
    });

    await page.goto('/project/test-project/ai-tools');
    const own = page.getByTestId('ai-activity-7');
    await expect(own).toContainText('execute_sql');
    await expect(own).toContainText('Codex MCP');
    await expect(page.getByTestId('ai-activity-6')).toContainText('Refused (403)');
    await expect(page.getByTestId('ai-activity-6').getByRole('button', { name: 'Revoke' })).toHaveCount(0);

    await own.getByRole('button', { name: 'Revoke' }).click();
    await page.getByTestId('confirm-modal').getByRole('button', { name: 'Revoke' }).click();
    await expect(page.getByText(/no ai tool has called this project yet/i)).toBeVisible();
    expect(revoked).toBe('hash-7');
  });

  test('read only narrows the MCP URL', async ({ page }) => {
    await page.route('**/api/auth/tokens', (route) =>
      route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ token: 'excb_ro', prefix: 'excb_ro', name: 'Cursor MCP', scopes: 'read', projectId: 'test-project' }),
      }),
    );
    await page.goto('/project/test-project/ai-tools');
    await page.getByRole('button', { name: /create token and show setup/i }).click();
    const setup = page.getByTestId('mcp-setup');
    await expect(setup).toContainText('.cursor/mcp.json');
    await expect(setup).toContainText('/mcp?project=test-project&read_only=true');
  });
});

test.describe('Connect your AI tool, with MCP dark', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockMcp(page, false);
  });

  test('the page is not available and the rail has no AI Tools', async ({ page }) => {
    await page.goto('/project/test-project/ai-tools');
    await expect(page.getByTestId('feature-unavailable')).toBeVisible();
    await expect(page.getByTestId('connect-ai-tool-page')).toHaveCount(0);
    await expect(page.getByTestId('nav-storage')).toBeVisible();
    await expect(page.getByTestId('nav-ai-tools')).toHaveCount(0);
  });
});
