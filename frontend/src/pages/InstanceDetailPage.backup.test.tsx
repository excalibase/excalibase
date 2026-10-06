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

async function openBackups() {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/provision/p-1') return { data: PROJECT } as never;
    if (url.includes('/backup/list')) return { data: { backups: [], backupEnabled: true, schedule: '', retentionDays: 7 } } as never;
    return { data: [] } as never;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <MemoryRouter initialEntries={['/project/p-1/database/overview']}>
        <Routes>
          <Route path="/project/:projectId/database/overview" element={<InstanceDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await userEvent.click(await screen.findByRole('button', { name: /backups/i }));
}

// EXC-555: Trigger Backup gave no sign either way.
describe('InstanceDetailPage — trigger backup', () => {
  beforeEach(() => vi.clearAllMocks());

  test('a started backup says so', async () => {
    await openBackups();
    vi.mocked(api.post).mockResolvedValue({ data: { name: 'backup-1' } } as never);
    await userEvent.click(screen.getByRole('button', { name: /trigger backup/i }));
    expect(await screen.findByTestId('backup-trigger-result')).toHaveTextContent(/started/i);
  });

  test("a refused backup shows the server's reason", async () => {
    await openBackups();
    vi.mocked(api.post).mockRejectedValue({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'a backup is already running' } },
    });
    await userEvent.click(screen.getByRole('button', { name: /trigger backup/i }));
    expect(await screen.findByTestId('backup-trigger-result')).toHaveTextContent('a backup is already running');
  });
});
