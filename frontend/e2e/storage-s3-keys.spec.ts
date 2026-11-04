import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { loginAs, mockProject } from './helpers';

// S3 access keys (EXC-630) ship dark; /api/config says whether they are on.
async function mockS3(page: Page, s3: boolean) {
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'cloud', features: { s3 } }),
    }),
  );
  await page.route('**/api/projects/test-project/storage/buckets', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 'b1', name: 'photos', public: false }]) }),
  );
  await page.route('**/api/projects/test-project/storage/buckets/photos/objects*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ objects: [] }) }),
  );
}

test.describe('Storage S3 access keys', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
  });

  test('hidden while the feature is off', async ({ page }) => {
    await mockS3(page, false);
    await page.goto('/project/test-project/storage');
    await expect(page.getByRole('button', { name: 'Delete bucket' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'S3 access' })).toHaveCount(0);
  });

  test('creates a key, shows the secret once, and revokes it', async ({ page }) => {
    await mockS3(page, true);
    let keys: unknown[] = [];
    let requested: Record<string, unknown> | null = null;
    let revoked = '';
    await page.route('**/api/projects/test-project/storage/s3-keys', async (route) => {
      if (route.request().method() === 'POST') {
        requested = route.request().postDataJSON();
        const key = { id: 's3k_1', name: 'nas', accessKeyId: 'EXS3AAAAAAAAAAAAAAAA', buckets: [], read: true, write: true, delete: false, createdAt: '2026-10-18T01:00:00Z' };
        keys = [key];
        return route.fulfill({
          status: 201, contentType: 'application/json',
          body: JSON.stringify({ ...key, secretAccessKey: 'e2e-secret-shown-once', endpoint: 'https://s3.example.test', region: 'us-east-1' }),
        });
      }
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ keys, endpoint: 'https://s3.example.test', region: 'us-east-1' }),
      });
    });
    await page.route('**/api/projects/test-project/storage/s3-keys/s3k_1', async (route) => {
      revoked = route.request().method();
      keys = [];
      return route.fulfill({ status: 204 });
    });

    await page.goto('/project/test-project/storage');
    await page.getByRole('button', { name: 'S3 access' }).click();
    await expect(page.getByTestId('s3-endpoint')).toHaveText('https://s3.example.test');
    await page.getByLabel('Key name').fill('nas');
    await page.getByLabel('Write').check();
    await page.getByRole('button', { name: /create key/i }).click();

    const notice = page.getByTestId('new-s3-key');
    await expect(notice).toContainText('e2e-secret-shown-once');
    await expect(notice).toContainText('force_path_style = true');
    expect(requested).toMatchObject({ name: 'nas', buckets: [], permissions: { read: true, write: true, delete: false } });
    await notice.getByRole('button', { name: /i have saved it/i }).click();
    await expect(page.getByText('e2e-secret-shown-once')).toHaveCount(0);

    const row = page.getByTestId('s3-key-s3k_1');
    await expect(row).toContainText('EXS3AAAAAAAAAAAAAAAA');
    page.once('dialog', (dialog) => dialog.accept());
    await row.getByRole('button', { name: 'Revoke' }).click();
    await expect(row).toHaveCount(0);
    expect(revoked).toBe('DELETE');
  });
});
