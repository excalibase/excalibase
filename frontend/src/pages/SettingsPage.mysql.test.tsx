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

function renderSettings() {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p-my') return Promise.resolve({ data: MYSQL_PROJECT } as never);
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
    for (const id of ['connect-section', 'public-port-card', 'cluster-settings-card', 'backup-config-card']) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument();
    }
    for (const url of ['/projects/p-my/db-endpoint', '/provision/p-my/credentials', '/provision/p-my/cluster']) {
      expect(api.get).not.toHaveBeenCalledWith(url);
    }
  });
});
