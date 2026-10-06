import { Page } from '@playwright/test';

/**
 * VaultGuard (added in feat/security-hardening-studio) wraps the entire
 * authenticated route tree. It blocks rendering until both /api/vault/status
 * and /api/auth/setup-status resolve. Every test that goes past /login must
 * stub these two endpoints, otherwise the page hangs on the loader. We bake
 * it into loginAs so existing specs don't have to know about it. Specs that
 * exercise the wizard (sealed/uninitialized vault, no admin) override these
 * routes after calling loginAs.
 */
export async function mockVaultGuardReady(page: Page) {
  await page.route('**/api/vault/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        initialized: true,
        sealed: false,
        threshold: 1,
        shares: 1,
        progress: 0,
        type: 'shamir',
      }),
    }),
  );
  await page.route('**/api/auth/setup-status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ hasAdmin: true }),
    }),
  );
}

export async function loginAs(page: Page, user = { id: '1', username: 'admin', email: 'admin@test.com', role: 'admin' }) {
  await page.addInitScript((u) => {
    localStorage.setItem('auth_user', JSON.stringify(u));
    localStorage.setItem('theme', 'dark');
  }, user);
  // VaultGuard renders a global loader until vault/status + setup-status
  // resolve; without these stubs every authenticated route hangs.
  await mockVaultGuardReady(page);
}

export async function mockCloudMode(page: Page) {
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'cloud' }),
    })
  );
}

export async function mockInstances(page: Page, projectId = 'test-project') {
  await page.route('**/api/provision', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{ projectId, databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'excalibase-test', status: 'ACTIVE', currentStage: 'COMPLETED', host: 'localhost', port: '5432', databaseName: 'testdb' }]),
      });
    }
    return route.continue();
  });
}

export async function mockProject(page: Page, projectId = 'test-project') {
  await mockInstances(page, projectId);
  await page.route(`**/api/provision/${projectId}`, (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ projectId, databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'excalibase-test', status: 'ACTIVE', currentStage: 'COMPLETED', host: 'localhost', port: '5432', databaseName: 'testdb', backupEnabled: false, backupSchedule: '', backupRetentionDays: 0, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' }),
    })
  );
}

/** Helper: match API path ignoring query params */
function apiRoute(page: Page, pathPattern: string, response: unknown, statusCode = 200) {
  return page.route('**' + pathPattern + '*', (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith(pathPattern) || url.pathname === pathPattern) {
      return route.fulfill({ status: statusCode, contentType: 'application/json', body: JSON.stringify(response) });
    }
    return route.continue();
  });
}

export async function mockSchemaEndpoints(page: Page, projectId = 'test-project') {
  const base = `/api/schema/${projectId}`;

  await apiRoute(page, `${base}/tables`, [
    { name: 'users', schema: 'public', type: 'BASE TABLE', comment: null },
    { name: 'orders', schema: 'public', type: 'BASE TABLE', comment: null },
  ]);

  await apiRoute(page, `${base}/tables/users/columns`, [
    { name: 'id', dataType: 'integer', nullable: false, primaryKey: true, unique: false, defaultValue: "nextval('users_id_seq')", ordinalPosition: 1, comment: null, characterMaximumLength: null, numericPrecision: 32, numericScale: 0 },
    { name: 'email', dataType: 'character varying', nullable: false, primaryKey: false, unique: true, defaultValue: null, ordinalPosition: 2, comment: null, characterMaximumLength: 255, numericPrecision: null, numericScale: null },
    { name: 'name', dataType: 'text', nullable: true, primaryKey: false, unique: false, defaultValue: null, ordinalPosition: 3, comment: null, characterMaximumLength: null, numericPrecision: null, numericScale: null },
  ]);

  // Row data for tables
  await page.route(new RegExp(`/api/schema/${projectId}/tables/.*/rows`), (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ columns: [{ name: 'id', dataType: 'INT4' }, { name: 'email', dataType: 'VARCHAR' }], rows: [[1, 'alice@test.com'], [2, 'bob@test.com']], totalCount: 2 }),
    })
  );

  await apiRoute(page, `${base}/relationships`, []);
  await apiRoute(page, `${base}/roles`, [
    { name: 'postgres', login: true, superuser: true, createDb: true, createRole: true, connLimit: -1 },
    { name: 'excalibase_app', login: true, superuser: false, createDb: false, createRole: false, connLimit: -1 },
  ]);
  await apiRoute(page, `${base}/extensions`, [
    { name: 'plpgsql', installedVersion: '1.0', defaultVersion: '1.0', schema: 'pg_catalog', comment: 'PL/pgSQL procedural language' },
    { name: 'uuid-ossp', installedVersion: null, defaultVersion: '1.1', schema: null, comment: 'generate universally unique identifiers' },
    { name: 'pgcrypto', installedVersion: null, defaultVersion: '1.3', schema: null, comment: 'cryptographic functions' },
  ]);
  await apiRoute(page, `${base}/policies`, []);
  await apiRoute(page, `${base}/functions`, []);
  await apiRoute(page, `${base}/query`, { columns: [{ name: '?column?', dataType: 'INT4' }], rows: [[1]] });

  // Edge functions
  await page.route('**/api/functions', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{ id: 'hello', name: 'Hello World', code: 'function handler(d) { return {msg:"hi"}; }', hookType: 'custom', active: true, version: 1, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' }]),
      });
    }
    return route.continue();
  });
  await apiRoute(page, '/api/functions/runtime/status', { status: 'healthy', healthy: true });

  // Triggers
  await apiRoute(page, `${base}/triggers`, []);

  // Types
  await apiRoute(page, `${base}/types`, [
    { name: 'status_enum', schema: 'public', type: 'enum', values: ['active', 'inactive'] },
  ]);

  // Advisors
  await apiRoute(page, `${base}/advisors/performance`, [
    { ruleId: '0001', severity: 'high', category: 'performance', title: 'Unindexed Foreign Key', description: 'FK without index', table: 'orders', fix: 'CREATE INDEX idx_orders_user_id ON orders(user_id);' },
  ]);
  await apiRoute(page, `${base}/advisors/security`, [
    { ruleId: '0013', severity: 'medium', category: 'security', title: 'RLS Disabled', description: 'Table without RLS', table: 'users' },
  ]);

  // Migrations
  await apiRoute(page, `/api/provision/${projectId}/migrations`, [
    { id: 'mig-1', projectId, version: 'V1', name: 'create_users', description: 'Initial users table', sql: 'CREATE TABLE users ...', status: 'APPLIED', appliedAt: '2026-01-01T00:00:00Z', executionTimeMs: 42, checksum: 'abc12345' },
  ]);

  // Logs
  await page.route(`**/api/provision/${projectId}/logs*`, (route) =>
    route.fulfill({ status: 200, contentType: 'text/plain', body: '2026-01-01 INFO Starting database...\n2026-01-01 WARNING Slow query detected\n2026-01-01 ERROR Connection refused' })
  );

  // Indexes for tables
  await apiRoute(page, `${base}/tables/users/indexes`, [
    { name: 'users_pkey', tableName: 'users', columns: ['id'], unique: true, type: 'btree' },
    { name: 'users_email_key', tableName: 'users', columns: ['email'], unique: true, type: 'btree' },
  ]);
  await apiRoute(page, `${base}/tables/orders/indexes`, []);

  // Auth users
  await apiRoute(page, `/api/projects/${projectId}/auth/users`, [
    { id: 1, email: 'alice@test.com', full_name: 'Alice', role: 'user', enabled: true, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', last_login_at: '2026-03-01T00:00:00Z' },
    { id: 2, email: 'bob@test.com', full_name: 'Bob', role: 'admin', enabled: false, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', last_login_at: null },
  ]);
  await apiRoute(page, `/api/projects/${projectId}/auth/sessions`, [
    { id: 1, user_id: 1, email: 'alice@test.com', expiry_date: '2027-01-01T00:00:00Z', created_at: '2026-03-01T00:00:00Z', revoked: false },
  ]);
}

/**
 * Gives the signed-in user an org role on the project, which is how Studio
 * decides who may edit API permissions (Developer+) and end-user roles (Admin+).
 * Registered after mockProject, so it wins for the project read.
 */
export async function mockProjectRole(page: Page, role: string, projectId = 'test-project') {
  await page.route('**/api/orgs', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([{ id: 'org-1', name: 'Org One', slug: 'org-one', tier: 'FREE', ownerId: '1', role }]),
    }),
  );
  await page.route(`**/api/provision/${projectId}`, (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'excalibase-test', status: 'ACTIVE', currentStage: 'COMPLETED', host: 'localhost', port: '5432', databaseName: 'testdb' }),
    }),
  );
}

export interface PermissionDocumentMock {
  projectId: string;
  version: number;
  tables: Array<Record<string, unknown> & { table: string; role: string }>;
  functions: Array<{ function: string; exposedAs: string; inferPermissions: boolean; sessionArgument: string | null }>;
  functionPermissions: Array<{ function: string; role: string }>;
}

export interface RecordedCall {
  method: string;
  path: string;
  body: unknown;
}

/**
 * A stateful permissions API: GET returns the document as the writes so far
 * left it, so the page re-reads what it just saved. Every write is recorded.
 */
export async function mockPermissionsApi(
  page: Page,
  doc: PermissionDocumentMock,
  options: { trackAnswer?: (fn: string) => { status: number; body: unknown } } = {},
  projectId = 'test-project',
) {
  const calls: RecordedCall[] = [];
  const base = `/api/provision/${projectId}`;
  const json = (status: number, body: unknown) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });

  await page.route(new RegExp(`${base}/(permissions|tracked-functions|function-permissions)/`), (route) => {
    const request = route.request();
    const path = decodeURIComponent(new URL(request.url()).pathname);
    const method = request.method();
    const body = request.postData() ? JSON.parse(request.postData() as string) : undefined;
    if (method !== 'GET') calls.push({ method, path, body });

    const table = /\/permissions\/tables\/([^/]+)\/roles\/([^/]+)\/([^/]+)$/.exec(path);
    if (table) {
      const [, name, role, op] = table;
      let entry = doc.tables.find((t) => t.table === name && t.role === role);
      if (method === 'PUT') {
        if (!entry) doc.tables.push((entry = { table: name, role }));
        entry[op] = body;
        doc.version += 1;
        return route.fulfill(json(200, body));
      }
      if (entry) delete entry[op];
      doc.version += 1;
      return route.fulfill({ status: 204 });
    }
    if (path === `${base}/permissions/`) return route.fulfill(json(200, doc));
    if (path === `${base}/tracked-functions/` && method === 'POST') {
      const request = body as { function: string; inferPermissions: boolean; sessionArgument: string | null };
      const answer = options.trackAnswer?.(request.function);
      if (answer && answer.status >= 400) return route.fulfill(json(answer.status, answer.body));
      const tracked = { ...request, exposedAs: 'QUERY' };
      doc.functions.push(tracked);
      return route.fulfill(json(201, { ...tracked, securityDefiner: false, ...(answer?.body as object) }));
    }
    const untrack = /\/tracked-functions\/([^/]+)$/.exec(path);
    if (untrack && method === 'DELETE') {
      doc.functions = doc.functions.filter((f) => f.function !== untrack[1]);
      doc.functionPermissions = doc.functionPermissions.filter((f) => f.function !== untrack[1]);
      return route.fulfill({ status: 204 });
    }
    const fnRole = /\/function-permissions\/([^/]+)\/roles\/([^/]+)$/.exec(path);
    if (fnRole) {
      const [, fn, role] = fnRole;
      doc.functionPermissions = doc.functionPermissions.filter((f) => !(f.function === fn && f.role === role));
      if (method === 'PUT') {
        doc.functionPermissions.push({ function: fn, role });
        return route.fulfill(json(200, { function: fn, role }));
      }
      return route.fulfill({ status: 204 });
    }
    return route.fulfill(json(404, { error: 'not found' }));
  });
  return calls;
}

/** Where the permission specs leave their screenshots. */
export const SHOTS_DIR = process.env.STUDIO_SHOTS_DIR ?? 'test-results/shots';
