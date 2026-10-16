import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { api } from '../api/client';
import { DatabaseRequired } from './DatabaseRequired';
import { DatabaseServiceCard } from './services/DatabaseServiceCard';
import { PROJECT_NAV, mysqlUnavailableReason, sectionLanding, visibleNavItems, visibleProjectNav } from './layout/navigation';
import { engineLabel } from '../utils/engine';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

// ADR 0041: a MySQL project shows its status and endpoint, and the pages that
// only work on Postgres are neither listed nor reachable.
const MYSQL_PROJECT = {
  projectId: 'p-my', orgId: 'o', databaseType: 'MYSQL', mysqlVersion: '8.4.10', tier: 'FREE',
  status: 'ACTIVE', currentStage: 'COMPLETED', host: 'moco-p-my-mysql-primary.o-p-my.svc.cluster.local',
  port: 3306, databaseName: 'app', namespace: 'o-p-my',
};

function renderWithProject(ui: React.ReactElement, path = '/project/p-my') {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p-my') return Promise.resolve({ data: MYSQL_PROJECT } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/project/:projectId" element={ui} />
          <Route path="/project/:projectId/sql" element={<DatabaseRequired mysql />}>
            <Route index element={<p>sql editor</p>} />
          </Route>
          <Route path="/project/:projectId/database/rls" element={<DatabaseRequired />}>
            <Route index element={<p>rls page</p>} />
          </Route>
          <Route path="/project/:projectId/operations/backups" element={<DatabaseRequired mysql />}>
            <Route index element={<p>backups page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

describe('MySQL projects in Studio', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the engine is named MySQL', () => {
    expect(engineLabel({ databaseType: 'MYSQL' })).toBe('MySQL');
  });

  // EXC-586: backups work on MySQL, so their page is reachable.
  test('the Backups page opens on a MySQL project', async () => {
    renderWithProject(<p>overview</p>, '/project/p-my/operations/backups');
    expect(await screen.findByText('backups page')).toBeInTheDocument();
    expect(screen.queryByTestId('postgres-required')).not.toBeInTheDocument();
  });

  test('the navigation lists only what works on MySQL', () => {
    const sections = visibleProjectNav({ appHosting: true, mcp: false, postgres: false });
    const keys = sections.map((section) => section.key);
    for (const hidden of ['auth', 'api-keys']) {
      expect(keys).not.toContain(hidden);
    }
    for (const shown of ['home', 'sql', 'schema', 'database', 'containers', 'storage', 'vault', 'settings', 'monitoring', 'operations']) {
      expect(keys).toContain(shown);
    }
    const monitoring = PROJECT_NAV.find((section) => section.key === 'monitoring');
    expect(visibleNavItems(monitoring?.children ?? [], false, false).map((item) => item.label)).toEqual(['Alerts']);
  });

  // ADR 0041 decision 9: the database pages that read MySQL stay, the rest say why not.
  test('Databases lists the pages that work on MySQL', () => {
    const database = PROJECT_NAV.find((section) => section.key === 'database');
    expect(visibleNavItems(database?.children ?? [], false, false).map((item) => item.label)).toEqual([
      'Tables', 'Functions', 'Indexes',
    ]);
    expect(visibleNavItems(database?.children ?? [], false, true).map((item) => item.label)).toEqual([
      'Overview', 'Tables', 'Functions', 'Extensions', 'Roles', 'RLS Policies', 'Triggers', 'Indexes', 'Types', 'Advisors',
    ]);
  });

  test('Databases opens on Tables for MySQL and on the overview for Postgres', () => {
    const database = PROJECT_NAV.find((section) => section.key === 'database');
    expect(database && sectionLanding(database, false)).toBe('database/tables');
    expect(database && sectionLanding(database, true)).toBe('database/overview');
  });

  test('every page hidden on MySQL has a one-line reason', () => {
    expect(mysqlUnavailableReason('database/extensions')).toMatch(/MySQL has no extensions/);
    expect(mysqlUnavailableReason('database/rls')).toMatch(/CREATE POLICY is Postgres row-level security/);
    expect(mysqlUnavailableReason('database/types')).toMatch(/MySQL has no user-defined types/);
    expect(mysqlUnavailableReason('database/advisors')).toMatch(/Postgres statistics catalogs/);
    expect(mysqlUnavailableReason('database/documents')).toMatch(/DocumentDB runs on Postgres/);
    expect(mysqlUnavailableReason('database/mongo-users')).toMatch(/DocumentDB runs on Postgres/);
    expect(mysqlUnavailableReason('database/roles')).toMatch(/created and rotated by the platform/);
    expect(mysqlUnavailableReason('database/triggers')).toMatch(/SQL editor/);
    expect(mysqlUnavailableReason('database/tables')).toBeUndefined();
    expect(mysqlUnavailableReason('somewhere/else')).toBeUndefined();
  });

  test('a Postgres project keeps every page', () => {
    const keys = visibleProjectNav({ appHosting: true, mcp: false, postgres: true }).map((section) => section.key);
    expect(keys).toEqual(expect.arrayContaining(['sql', 'schema', 'database', 'auth', 'api-keys', 'operations']));
  });

  test('the SQL editor opens on a MySQL project', async () => {
    renderWithProject(<p>home</p>, '/project/p-my/sql');
    expect(await screen.findByText('sql editor')).toBeInTheDocument();
  });

  test('a Postgres-only page says why instead of failing', async () => {
    renderWithProject(<p>home</p>, '/project/p-my/database/rls');
    const notice = await screen.findByTestId('postgres-required');
    expect(notice).toHaveTextContent(/not available for MySQL projects/i);
    expect(screen.getByTestId('postgres-required-reason')).toHaveTextContent(/CREATE POLICY is Postgres row-level security/);
    expect(screen.queryByText('rls page')).not.toBeInTheDocument();
  });

  test('the overview shows the MySQL version and where to connect', async () => {
    renderWithProject(<DatabaseServiceCard projectId="p-my" />);
    expect(await screen.findByText('MySQL 8.4.10')).toBeInTheDocument();
    expect(screen.getByTestId('service-database-mysql-endpoint')).toHaveTextContent(
      'moco-p-my-mysql-primary.o-p-my.svc.cluster.local:3306'
    );
    expect(screen.getByTestId('service-database-mysql-endpoint')).toHaveTextContent(/TLS required/i);
    expect(screen.queryByText('Open database')).not.toBeInTheDocument();
    // No generated API serves a MySQL project yet.
    expect(screen.queryByText(/GraphQL and REST API/)).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/provision/p-my/db-endpoint');
  });
});
