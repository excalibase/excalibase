import { test, expect } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

// Non-secret placeholder used only by mocked HTTP responses in these tests.
const MOCK_CREDENTIAL_PLACEHOLDER = ['mock', 'fixture', 'value'].join('-');

function mockVaultEndpoints(page: import('@playwright/test').Page) {
  // Vault status
  page.route('**/api/vault/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ initialized: true, sealed: false }),
    })
  );

  // Vault secrets list
  page.route('**/api/vault/secrets-list*', (route) => {
    const url = new URL(route.request().url());
    const prefix = url.searchParams.get('prefix') || '';
    const allPaths = [
      'projects/duc-corp/app-a/credentials/admin',
      'projects/duc-corp/app-a/credentials/auth_admin',
      'projects/duc-corp/app-a/credentials/excalibase_app',
      'projects/org-b/app-b/credentials/admin',
      'backup/s3',
    ];
    const filtered = prefix ? allPaths.filter((p) => p.startsWith(prefix)) : allPaths;
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ paths: filtered }),
    });
  });

  // Vault secret read
  page.route('**/api/vault/secrets/**', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          host: 'app-a-postgres-rw.svc.cluster.local',
          port: '5432',
          username: 'admin',
          password: MOCK_CREDENTIAL_PLACEHOLDER,
          database: 'app',
        }),
      });
    }
    if (route.request().method() === 'DELETE') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'ok' }),
      });
    }
    return route.continue();
  });
}

function mockVaultSealed(page: import('@playwright/test').Page) {
  page.route('**/api/vault/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ initialized: true, sealed: true }),
    })
  );
}

test.describe('Vault Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockVaultEndpoints(page);
  });

  test('renders vault page with secrets list', async ({ page }) => {
    await page.goto('/project/test-project/vault');
    await expect(page.getByTestId('vault-page')).toBeVisible();
    await expect(page.getByTestId('vault-secrets-list')).toBeVisible();
    // Should show 5 secrets (pki/* filtered out)
    const rows = page.locator('[data-testid^="vault-secret-row-"]');
    await expect(rows).toHaveCount(5);
  });

  test('search filters secrets', async ({ page }) => {
    await page.goto('/project/test-project/vault');
    await page.getByTestId('vault-search').fill('org-b');
    const rows = page.locator('[data-testid^="vault-secret-row-"]');
    await expect(rows).toHaveCount(1);
  });

  test('reveal shows secret values', async ({ page }) => {
    await page.goto('/project/test-project/vault');
    // Click reveal on first secret
    const revealBtn = page.locator('[data-testid^="vault-reveal-"]').first();
    await revealBtn.click();
    // Secret values should be visible
    await expect(page.getByTestId('vault-secret-values')).toBeVisible();
    // Should show host, port, etc.
    await expect(page.getByText('app-a-postgres-rw.svc.cluster.local')).toBeVisible();
  });

  test('hide button hides revealed secret', async ({ page }) => {
    await page.goto('/project/test-project/vault');
    // Reveal
    const revealBtn = page.locator('[data-testid^="vault-reveal-"]').first();
    await revealBtn.click();
    await expect(page.getByTestId('vault-secret-values')).toBeVisible();
    // Click again to hide
    await revealBtn.click();
    await expect(page.getByTestId('vault-secret-values')).not.toBeVisible();
  });

  test('sealed vault redirects to /setup wizard', async ({ page }) => {
    // VaultGuard (added in feat/security-hardening-studio) intercepts at
    // the route level: when vault is sealed, the user is redirected to
    // the /setup wizard's unseal step instead of seeing an inline
    // "Vault is Sealed" message on VaultPage. The new mock must include
    // the threshold/progress fields VaultGuard reads.
    await page.route('**/api/vault/status', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          initialized: true,
          sealed: true,
          threshold: 1,
          shares: 1,
          progress: 0,
          type: 'shamir',
        }),
      }),
    );
    await page.goto('/project/test-project/vault');
    await expect(page).toHaveURL(/\/setup$/);
    await expect(page.getByTestId('vault-setup-unseal')).toBeVisible();
  });

  test('vault appears in sidebar navigation', async ({ page }) => {
    await page.goto('/project/test-project/vault');
    // Vault icon should be highlighted in the sidebar (active state)
    await expect(page.getByTestId('vault-page')).toBeVisible();
    // Breadcrumb shows "Vault"
    await expect(page.getByText('Vault', { exact: true })).toBeVisible();
  });

  test('password field is masked in revealed view', async ({ page }) => {
    await page.goto('/project/test-project/vault');
    const revealBtn = page.locator('[data-testid^="vault-reveal-"]').first();
    await revealBtn.click();
    await expect(page.getByTestId('vault-secret-values')).toBeVisible();
    // Password should show dots, not actual value
    await expect(page.getByText('••••••••')).toBeVisible();
    // But host should show actual value
    await expect(page.getByText('app-a-postgres-rw.svc.cluster.local')).toBeVisible();
  });
});
