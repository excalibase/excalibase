import { test, expect } from '@playwright/test';
import { loginAs, mockProject } from './helpers';

test.describe('Connect your AI tool', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
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
