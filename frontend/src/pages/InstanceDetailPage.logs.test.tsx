import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { InstanceDetailPage } from './InstanceDetailPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const PROJECT = {
  projectId: 'p-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', currentStage: 'COMPLETED',
  namespace: 'ns', host: 'h', port: '5432', databaseName: 'app', createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
};

async function openLogs(logs: string) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/provision/p-1') return { data: PROJECT } as never;
    if (url.includes('/logs')) return { data: { logs } } as never;
    return { data: [] } as never;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/project/p-1/database/overview']}>
        <Routes>
          <Route path="/project/:projectId/database/overview" element={<InstanceDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await userEvent.click(await screen.findByRole('button', { name: /^logs$/i }));
}

// A database quiet for over an hour is not a pod that is not running (EXC-684).
describe('InstanceDetailPage logs', () => {
  beforeEach(() => vi.clearAllMocks());

  test('searches the widest window the server keeps', async () => {
    await openLogs('line one\n');
    await screen.findByText(/line one/);
    const asked = vi.mocked(api.get).mock.calls.map(([url]) => String(url)).filter((url) => url.includes('/logs'));
    expect(asked.some((url) => url.includes('since=7d'))).toBe(true);
  });

  test('an empty answer says there were no logs in the window, not that the pod is down', async () => {
    await openLogs('');
    expect(await screen.findByText(/No logs in the last 7 days/i)).toBeInTheDocument();
    expect(screen.queryByText(/pod may not be running/i)).not.toBeInTheDocument();
  });
});
