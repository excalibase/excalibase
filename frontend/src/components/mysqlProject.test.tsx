import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { api } from '../api/client';
import { DatabaseRequired } from './DatabaseRequired';
import { DatabaseServiceCard } from './services/DatabaseServiceCard';
import { PROJECT_NAV, visibleNavItems, visibleProjectNav } from './layout/navigation';
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
          <Route path="/project/:projectId/sql" element={<DatabaseRequired />}>
            <Route index element={<p>sql editor</p>} />
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
    for (const hidden of ['sql', 'schema', 'database', 'auth', 'api-keys']) {
      expect(keys).not.toContain(hidden);
    }
    for (const shown of ['home', 'containers', 'storage', 'vault', 'settings', 'monitoring', 'operations']) {
      expect(keys).toContain(shown);
    }
    const monitoring = PROJECT_NAV.find((section) => section.key === 'monitoring');
    expect(visibleNavItems(monitoring?.children ?? [], false, false).map((item) => item.label)).toEqual(['Alerts']);
  });

  test('a Postgres project keeps every page', () => {
    const keys = visibleProjectNav({ appHosting: true, mcp: false, postgres: true }).map((section) => section.key);
    expect(keys).toEqual(expect.arrayContaining(['sql', 'schema', 'database', 'auth', 'api-keys', 'operations']));
  });

  test('a Postgres-only page says so instead of failing', async () => {
    renderWithProject(<p>home</p>, '/project/p-my/sql');
    expect(await screen.findByTestId('postgres-required')).toHaveTextContent(/not available for MySQL projects/i);
    expect(screen.queryByText('sql editor')).not.toBeInTheDocument();
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
