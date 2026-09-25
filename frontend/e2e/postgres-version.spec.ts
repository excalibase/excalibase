import { test, expect, Page } from '@playwright/test';
import { loginAs, mockInstances, mockSchemaEndpoints } from './helpers';

const PROJECT_ID = 'test-project';
// The dev server compiles lazy routes on first request, which is slow when
// several workers ask at once.
const FIRST_RENDER = 20_000;

const CATALOG = {
  documentDbRef: 'v0.117-0',
  majors: [
    { major: '14', available: true, documentDb: false, documentDbUnavailableReason: 'PostgreSQL 14 cannot carry DocumentDB.' },
    { major: '16', available: false, documentDb: true },
    { major: '17', available: true, documentDb: true },
  ],
};

const CREDENTIALS = {
  projectId: PROJECT_ID,
  host: 'test-project-postgres-rw.org-1.svc.cluster.local',
  port: 5432,
  databaseName: 'app',
  username: 'excalibase_app',
  password: 's3cret',
  sslMode: 'require',
  connectionUrl: '',
};

const INTERNAL = {
  host: 'test-project-postgres-rw.org-1.svc.cluster.local',
  port: 5432,
  connectionString: 'postgresql://excalibase_app@test-project-postgres-rw.org-1.svc.cluster.local:5432/app?sslmode=prefer',
};

async function json(page: Page, pattern: string, body: unknown) {
  await page.route(pattern, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

async function mockSettingsProject(page: Page, documentDb: boolean) {
  await mockInstances(page, PROJECT_ID);
  await mockSchemaEndpoints(page, PROJECT_ID);
  await json(page, '**/api/postgres/catalog', CATALOG);
  await json(page, `**/api/provision/${PROJECT_ID}`, {
    projectId: PROJECT_ID, databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'org-1-test-project',
    status: 'ACTIVE', currentStage: 'COMPLETED', host: INTERNAL.host, port: 5432, databaseName: 'app',
    postgresVersion: '17', documentDb, backupEnabled: false, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
  });
  await json(page, `**/api/provision/${PROJECT_ID}/credentials`, CREDENTIALS);
  await json(page, `**/api/projects/${PROJECT_ID}/db-endpoint`, {
    projectId: PROJECT_ID, publicEnabled: true, available: true, host: 'test-project.db.example.com', port: 30100,
    requireTls: true, database: 'app', username: 'excalibase_app',
    connectionStrings: { requireTls: '', allowPlaintext: '' },
    caCertificate: '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n',
    internal: documentDb ? { ...INTERNAL, mongoPort: 10260 } : INTERNAL,
    ...(documentDb ? { mongoPort: 30101, mongoAvailable: true } : {}),
  });
}

test.describe('Postgres version picker', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await json(page, '**/api/postgres/catalog', CATALOG);
    await json(page, '**/api/orgs', [{ id: 'org-1', name: 'Org One', slug: 'org-one' }]);
    await page.goto('/provision');
    await expect(page.getByTestId('pg-version-selector')).toBeVisible({ timeout: FIRST_RENDER });
  });

  test('offers the catalogue majors with none chosen and an unpublished one disabled', async ({ page }) => {
    await expect(page.getByTestId('pg-version-14')).toBeVisible();
    await expect(page.getByTestId('pg-version-17')).toHaveAttribute('aria-pressed', 'false');
    await expect(page.getByTestId('pg-version-14')).toHaveAttribute('aria-pressed', 'false');
    await expect(page.getByTestId('pg-version-16')).toBeDisabled();
  });

  test('keeps DocumentDB off with the reason on a major that cannot carry it', async ({ page }) => {
    await page.getByTestId('pg-version-14').click();
    await expect(page.getByTestId('documentdb-toggle')).toBeDisabled();
    await expect(page.getByTestId('documentdb-reason')).toBeVisible();

    await page.getByTestId('pg-version-17').click();
    await expect(page.getByTestId('documentdb-toggle')).toBeEnabled();
  });
});

test.describe('Project connection strings and minor upgrade', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
  });

  test('shows the Mongo strings of a DocumentDB project', async ({ page }) => {
    await mockSettingsProject(page, true);
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('connection-strings')).toBeVisible({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('conn-postgres-public')).toContainText('@test-project.db.example.com:30100/app');
    await expect(page.getByTestId('conn-mongo-public')).toContainText('@test-project.db.example.com:30101/?tls=true');
    await expect(page.getByTestId('conn-mongo-internal')).toContainText(':10260/');
  });

  test('offers nothing Mongo-shaped for a project created without DocumentDB', async ({ page }) => {
    await mockSettingsProject(page, false);
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('connection-strings')).toBeVisible({ timeout: FIRST_RENDER });
    await expect(page.getByTestId('conn-postgres-public')).toBeVisible();
    await expect(page.getByTestId('conn-mongo-section')).toHaveCount(0);
  });

  test('requests a minor upgrade and reports what the server answered', async ({ page }) => {
    await mockSettingsProject(page, false);
    await page.route(`**/api/provision/${PROJECT_ID}/upgrade`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ projectId: PROJECT_ID, status: 'ACTIVE', currentStage: 'COMPLETED', postgresVersion: '17' }),
      }),
    );
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('connection-strings')).toBeVisible({ timeout: FIRST_RENDER });
    await page.getByTestId('minor-upgrade-btn').click();
    await page.getByTestId('modal-confirm').click();
    await expect(page.getByTestId('minor-upgrade-result')).toContainText('PostgreSQL 17');
  });
});
