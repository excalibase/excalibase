import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const MYSQL_PROJECT = {
  projectId: 'p-my', orgId: 'o', databaseType: 'MYSQL', mysqlVersion: '8.4.9', tier: 'FREE', status: 'ACTIVE',
  host: 'moco-p-my-mysql-primary.o-p-my.svc.cluster.local', port: 3306, databaseName: 'app', namespace: 'o-p-my',
  deletionProtection: true,
};

const MYSQL_CREDENTIALS = {
  projectId: 'p-my', host: MYSQL_PROJECT.host, port: 3306, databaseName: 'app', username: 'admin',
  password: 's3cr3t', sslMode: 'REQUIRED', engine: 'mysql',
  connectionUrl: `mysql://admin:s3cr3t@${MYSQL_PROJECT.host}:3306/app?ssl-mode=REQUIRED`,
  jdbcUrl: `jdbc:mysql://${MYSQL_PROJECT.host}:3306/app?sslMode=REQUIRED`,
};

// EXC-585: published on its own port, TLS always required, no CA to offer.
const MYSQL_ENDPOINT = {
  projectId: 'p-my', publicOffered: true, publicEnabled: true, available: true, canChange: true,
  host: 'p-my.db.example.com', port: 32010, requireTls: true, engine: 'mysql', database: 'app', username: 'admin',
  connectionStrings: { requireTls: 'mysql://admin@p-my.db.example.com:32010/app?ssl-mode=REQUIRED', allowPlaintext: '' },
  caCertificate: '',
  internal: { host: MYSQL_PROJECT.host, port: 3306, connectionString: '' },
};

function renderSettings() {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p-my') return Promise.resolve({ data: MYSQL_PROJECT } as never);
    if (url === '/provision/p-my/credentials') return Promise.resolve({ data: MYSQL_CREDENTIALS } as never);
    if (url === '/projects/p-my/db-endpoint') return Promise.resolve({ data: MYSQL_ENDPOINT } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-my/settings']}>
        <Routes>
          <Route path="/project/:projectId/settings" element={<SettingsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// ADR 0041: a MySQL project's settings keep its lifecycle (pause, resume,
// minor upgrade, delete) and drop the Postgres-only cards.
describe('SettingsPage — MySQL', () => {
  beforeEach(() => vi.clearAllMocks());

  test('keeps the lifecycle and the MySQL version, drops the Postgres cards', async () => {
    renderSettings();
    expect(await screen.findByTestId('lifecycle-section')).toBeInTheDocument();
    expect(screen.getByTestId('minor-upgrade-card')).toHaveTextContent('MySQL 8.4.9');
    expect(screen.getByTestId('minor-upgrade-btn')).toBeEnabled();
    expect(screen.getAllByText('MySQL Version').length).toBeGreaterThan(0);
    expect(screen.queryByText('PostgreSQL Version')).not.toBeInTheDocument();
    for (const id of ['connect-section', 'cluster-settings-card', 'backup-config-card']) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument();
    }
    for (const url of ['/provision/p-my/cluster']) {
      expect(api.get).not.toHaveBeenCalledWith(url);
    }
  });

  // EXC-584: the owner's login, as MySQL clients take it, TLS required.
  test('shows the MySQL login and its connection strings, password masked', async () => {
    renderSettings();
    expect(await screen.findByTestId('conn-mysql-section')).toBeInTheDocument();
    expect(screen.getByTestId('conn-username')).toHaveTextContent('admin');
    expect(screen.getByTestId('conn-mysql-internal')).toHaveTextContent(
      `mysql://admin:••••••••@${MYSQL_PROJECT.host}:3306/app?ssl-mode=REQUIRED`,
    );
    expect(screen.getByTestId('conn-mysql-jdbc')).toHaveTextContent(
      `jdbc:mysql://${MYSQL_PROJECT.host}:3306/app?sslMode=REQUIRED`,
    );
    expect(screen.getByTestId('conn-mysql-section')).toHaveTextContent('TLS required');
    expect(screen.queryByTestId('conn-postgres-section')).not.toBeInTheDocument();
    expect(screen.queryByText(/postgresql:\/\//)).not.toBeInTheDocument();
  });

  test('offers the public port and its MySQL string, TLS required, no CA', async () => {
    renderSettings();
    expect(await screen.findByTestId('public-port-card')).toBeInTheDocument();
    expect(await screen.findByTestId('conn-mysql-public')).toHaveTextContent(
      'mysql://admin:••••••••@p-my.db.example.com:32010/app?ssl-mode=REQUIRED',
    );
    expect(screen.queryByTestId('conn-ca-download')).not.toBeInTheDocument();
  });
});
