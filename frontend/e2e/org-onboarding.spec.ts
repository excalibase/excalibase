import { test, expect, Page } from '@playwright/test';
import { loginAs, mockCloudMode } from './helpers';

// The dev server compiles lazy routes on first request.
const FIRST_RENDER = 20_000;

const CATALOG = { documentDbRef: 'v0.117-0', majors: [{ major: '17', available: true, documentDb: true }] };
const NEW_ORG = { id: 'org-new', name: 'Ada Labs', slug: 'ada-labs', tier: 'FREE', ownerId: '1' };

async function json(page: Page, pattern: string, body: unknown) {
  await page.route(pattern, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

// EXC-553: someone with no organization who opens "create project" is told
// what is missing and taken to create one, instead of a button that does nothing.
test.describe('Create project with no organization', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page, { id: '1', username: 'ada', email: 'ada@test.com', role: 'user' });
    await mockCloudMode(page);
    await json(page, '**/api/postgres/catalog', CATALOG);
    await json(page, '**/api/tiers', []);
    await json(page, '**/api/provision', []);
    let created = false;
    await page.route('**/api/orgs', (route) => {
      if (route.request().method() === 'POST') {
        created = true;
        return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify(NEW_ORG) });
      }
      return route.fulfill({
        status: 200, contentType: 'application/json', body: JSON.stringify(created ? [NEW_ORG] : []),
      });
    });
    await json(page, `**/api/orgs/${NEW_ORG.id}`, NEW_ORG);
    await json(page, `**/api/orgs/${NEW_ORG.id}/members`, []);
    await json(page, `**/api/orgs/${NEW_ORG.id}/invites`, []);
  });

  test('explains the missing organization and leads to creating one', async ({ page }) => {
    let provisionPosted = false;
    page.on('request', (request) => {
      if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/provision') provisionPosted = true;
    });
    await page.goto('/provision');

    await expect(page.getByTestId('provision-no-orgs')).toBeVisible({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('provision-no-orgs')).toContainText('not in one yet');

    const submit = page.getByTestId('provision-submit');
    await expect(submit).toBeEnabled();
    await page.getByLabel('Project Name').fill('shop');
    await submit.click();
    await expect(page.getByTestId('org-error')).toContainText('An organization is required');
    await expect(page.getByTestId('version-error')).toBeVisible();
    expect(provisionPosted).toBe(false);

    await page.getByTestId('org-error').getByRole('link', { name: 'Create an organization' }).click();
    await expect(page).toHaveURL(/\/orgs\?new=1$/);
    await page.getByLabel('Name').fill('Ada Labs');
    await page.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/orgs/${NEW_ORG.id}$`));

    await page.goto('/provision');
    await expect(page.getByLabel('Organization')).toHaveValue(NEW_ORG.id, { timeout: FIRST_RENDER });
    await expect(page.getByTestId('provision-no-orgs')).toHaveCount(0);
  });
});
