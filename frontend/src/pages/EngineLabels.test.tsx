import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { DashboardPage } from './DashboardPage';
import { InstancesPage } from './InstancesPage';
import { InstanceDetailPage } from './InstanceDetailPage';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

function instance(projectId: string, documentDb: boolean): DatabaseInstance {
  return {
    id: 1, projectId, orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: `o-1-${projectId}`,
    host: 'h', port: 5432, databaseName: 'app', username: 'app', status: 'ACTIVE', currentStage: 'COMPLETED',
    backupEnabled: false, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z', documentDb,
  } as DatabaseInstance;
}

function renderWith(ui: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p-docs') return Promise.resolve({ data: instance('p-docs', true) } as never);
    if (url === '/provision') return Promise.resolve({ data: [instance('p-docs', true), instance('p-pg', false)] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
});

describe('project lists name a DocumentDB project as one', () => {
  for (const [name, page] of [['dashboard', <DashboardPage key="d" />], ['instances', <InstancesPage key="i" />]] as const) {
    test(name, async () => {
      renderWith(page);
      const docsRow = (await screen.findByText('p-docs')).closest('tr') as HTMLElement;
      const pgRow = screen.getByText('p-pg').closest('tr') as HTMLElement;
      expect(within(docsRow).getByText(/DocumentDB \(MongoDB-compatible\)/)).toBeInTheDocument();
      expect(within(pgRow).getByText(/PostgreSQL/)).toBeInTheDocument();
      expect(within(pgRow).queryByText(/DocumentDB/)).not.toBeInTheDocument();
    });
  }
});

test('the project header names a DocumentDB project as one', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-docs']}>
        <Routes>
          <Route path="/project/:projectId" element={<InstanceDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
  expect(await screen.findByText(/DocumentDB \(MongoDB-compatible\) · FREE/)).toBeInTheDocument();
});
