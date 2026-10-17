import { test, expect, Page } from '@playwright/test';
import { loginAs } from './helpers';

// ADR 0041 decision 9: Studio's pages follow a MySQL project's engine.
const FIRST_RENDER = 20_000;
const PROJECT_ID = 'shop-mysql';
const HOST = 'moco-shop-mysql-mysql-primary.org-1-shop-mysql.svc.cluster.local';
const CA_PEM = '-----BEGIN CERTIFICATE-----\nMYSQLCA\n-----END CERTIFICATE-----\n';

async function json(page: Page, pattern: string, body: unknown) {
  await page.route(pattern, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }),
  );
}

async function mockMySQLProject(page: Page) {
  await loginAs(page);
  await json(page, '**/api/config', { deploymentMode: 'cloud', features: { mysql: true } });
  await json(page, '**/api/orgs', [{ id: 'org-1', name: 'Org One', slug: 'org-one', tier: 'FREE', ownerId: '1' }]);
  await json(page, `**/api/provision/${PROJECT_ID}`, {
    projectId: PROJECT_ID, orgId: 'org-1', databaseType: 'MYSQL', mysqlVersion: '8.4.10', tier: 'FREE',
    namespace: 'org-1-shop-mysql', status: 'ACTIVE', currentStage: 'COMPLETED', host: HOST, port: 3306,
    databaseName: 'shop', createdAt: '2026-10-11T00:00:00Z', updatedAt: '2026-10-11T00:00:00Z',
  });
  await json(page, `**/api/schema/${PROJECT_ID}/tables?*`, [{ name: 'orders', schema: 'shop', type: 'BASE TABLE', comment: null }]);
  await json(page, `**/api/schema/${PROJECT_ID}/tables`, [{ name: 'orders', schema: 'shop', type: 'BASE TABLE', comment: null }]);
}

test.describe('Studio on a MySQL project', () => {
  test('lists the MySQL database pages and says why the others are not there', async ({ page }) => {
    await mockMySQLProject(page);
    await page.goto(`/project/${PROJECT_ID}`);
    await page.getByTestId('nav-database').click({ timeout: FIRST_RENDER });
    await expect(page).toHaveURL(new RegExp(`/project/${PROJECT_ID}/database/tables$`));
    const subNav = page.getByTestId('sub-nav');
    for (const shown of ['Tables', 'Functions', 'Indexes']) {
      await expect(subNav.getByRole('link', { name: shown })).toBeVisible({ timeout: FIRST_RENDER });
    }
    for (const hidden of ['Extensions', 'RLS Policies', 'Types', 'Advisors', 'Roles', 'Triggers']) {
      await expect(subNav.getByRole('link', { name: hidden })).toHaveCount(0);
    }
    await page.goto(`/project/${PROJECT_ID}/database/rls`);
    await expect(page.getByTestId('postgres-required-reason')).toContainText('CREATE POLICY is Postgres row-level security', {
      timeout: FIRST_RENDER,
    });
    await page.goto(`/project/${PROJECT_ID}/database/functions`);
    await expect(page.getByTestId('mysql-functions-notice')).toContainText('MySQL functions return scalars', { timeout: FIRST_RENDER });
  });

  test('runs SQL in the MySQL dialect and warns that DDL is not rolled back', async ({ page }) => {
    await mockMySQLProject(page);
    let sent: unknown;
    await page.route(`**/api/schema/${PROJECT_ID}/query`, (route) => {
      sent = route.request().postDataJSON();
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          columns: [{ name: 'paid', dataType: 'TINYINT' }], rows: [[1]],
          notice: 'MySQL DDL is not transactional: statements before a failure stay applied.',
        }),
      });
    });
    await page.goto(`/project/${PROJECT_ID}/sql`);
    await expect(page.getByTestId('sql-dialect')).toHaveText('MySQL', { timeout: FIRST_RENDER });
    await expect(page.getByTestId('sql-mysql-ddl-note')).toContainText('not rolled back');
    await page.locator('.cm-content').click();
    await page.keyboard.press('ControlOrMeta+a');
    await page.keyboard.type('SELECT `paid` FROM orders');
    await page.getByTestId('run-query-btn').click();
    await expect(page.getByTestId('query-results')).toContainText('paid');
    await expect(page.getByTestId('query-notice')).toContainText('statements before a failure stay applied');
    expect(sent).toEqual({ query: 'SELECT `paid` FROM orders' });
  });

  test('creates a table with MySQL column types', async ({ page }) => {
    await mockMySQLProject(page);
    let created: unknown;
    await page.route(`**/api/schema/${PROJECT_ID}/tables`, (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      created = route.request().postDataJSON();
      return route.fulfill({ status: 201, contentType: 'application/json', body: '{"status":"created"}' });
    });
    await page.goto(`/project/${PROJECT_ID}/database/tables`);
    await page.getByTestId('new-table-btn').click({ timeout: FIRST_RENDER });
    await expect(page.getByLabel('Column type').first()).toHaveValue('bigint unsigned auto_increment');
    await expect(page.locator('#column-type-options option[value="tinyint(1)"]')).toHaveCount(1);
    await expect(page.locator('#column-type-options option[value="timestamptz"]')).toHaveCount(0);
    await page.getByTestId('table-name-input').fill('payments');
    await page.getByTestId('create-table-submit').click();
    await expect.poll(() => created).toMatchObject({
      name: 'payments', columns: [{ name: 'id', type: 'bigint unsigned auto_increment', primaryKey: true }],
    });
  });

  // EXC-589: every MySQL string verifies the server against the project CA.
  test('shows verifying mysql://, Node and Connector/J examples and the CA', async ({ page }) => {
    await mockMySQLProject(page);
    await json(page, `**/api/provision/${PROJECT_ID}/credentials`, {
      projectId: PROJECT_ID, host: HOST, port: 3306, databaseName: 'shop', username: 'admin', password: 's3cret',
      engine: 'mysql', jdbcUrl: `jdbc:mysql://${HOST}:3306/shop?sslMode=VERIFY_IDENTITY`,
    });
    await json(page, `**/api/projects/${PROJECT_ID}/db-endpoint`, {
      projectId: PROJECT_ID, publicOffered: true, publicEnabled: false, available: false, host: '', port: 0,
      requireTls: true, database: 'shop', username: 'admin', connectionStrings: { requireTls: '', allowPlaintext: '' },
      caCertificate: CA_PEM, internal: { host: HOST, port: 3306, connectionString: '' },
    });
    await page.goto(`/project/${PROJECT_ID}/settings`);
    await expect(page.getByTestId('conn-mysql-internal')).toContainText(
      `@${HOST}:3306/shop?ssl-mode=VERIFY_IDENTITY&ssl-ca=${PROJECT_ID}-ca.crt`,
      { timeout: FIRST_RENDER },
    );
    await expect(page.getByTestId('conn-mysql-section').getByTestId('conn-ca-download')).toHaveAttribute(
      'download', `${PROJECT_ID}-ca.crt`,
    );
    await expect(page.getByTestId('conn-mysql-connectorj')).toContainText('sslMode=VERIFY_IDENTITY');
    await expect(page.getByTestId('conn-mysql-node')).toContainText("import mysql from 'mysql2/promise'");
    await expect(page.getByTestId('conn-mysql-node')).toContainText(`ssl: { ca: readFileSync('${PROJECT_ID}-ca.crt') }`);
    await expect(page.getByTestId('conn-mysql-node')).not.toContainText('rejectUnauthorized');
    await expect(page.getByTestId('conn-mysql-connectorj')).toContainText('DriverManager.getConnection(url, "admin"');
    await expect(page.getByTestId('conn-mysql-node')).not.toContainText('s3cret');
  });
});
