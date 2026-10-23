import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { api } from '../api/client';
import { DatabaseRequired } from './DatabaseRequired';
import { PROJECT_NAV, visibleNavItems } from './layout/navigation';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

// EXC-655: the advisors check MySQL too once their feature is on; until then
// the page stays the Postgres-only one it was.
const MYSQL_PROJECT = { projectId: 'p-my', orgId: 'o', databaseType: 'MYSQL', status: 'ACTIVE', currentStage: 'COMPLETED' };

function renderAdvisorsRoute(features: Record<string, boolean>) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p-my') return Promise.resolve({ data: MYSQL_PROJECT } as never);
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-my/database/advisors']}>
        <Routes>
          <Route path="/project/:projectId/database/advisors" element={<DatabaseRequired mysqlFeature="advisors" />}>
            <Route index element={<p>advisors page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const databaseItems = PROJECT_NAV.find((section) => section.key === 'database')?.children ?? [];

describe('Advisors on a MySQL project', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the navigation lists Advisors on MySQL only with the feature on', () => {
    const labels = (features: Record<string, boolean>) => visibleNavItems(databaseItems, false, false, features).map((item) => item.label);
    expect(labels({})).not.toContain('Advisors');
    expect(labels({ advisors: true })).toContain('Advisors');
    expect(visibleNavItems(databaseItems, false, true, {}).map((item) => item.label)).toContain('Advisors');
  });

  test('the page opens on MySQL with the feature on', async () => {
    renderAdvisorsRoute({ advisors: true });
    expect(await screen.findByText('advisors page')).toBeInTheDocument();
  });

  test('the page explains it is Postgres-only with the feature off', async () => {
    renderAdvisorsRoute({});
    expect(await screen.findByTestId('postgres-required')).toBeInTheDocument();
    expect(screen.queryByText('advisors page')).not.toBeInTheDocument();
  });
});
