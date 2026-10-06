import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { MigrationsPage } from './MigrationsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const applied = {
  id: 'm1', projectId: 'p1', version: 'V1', name: 'init', description: 'first tables', sql: 'select 1',
  status: 'APPLIED', appliedAt: '2026-10-01T12:00:00Z', executionTimeMs: 12, checksum: 'abcdef0123',
};

function renderPage(history: unknown[] = [applied]) {
  vi.mocked(api.get).mockResolvedValue({ data: history } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/migrations']}>
        <Routes>
          <Route path="/project/:projectId/migrations" element={<MigrationsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function applyNamed(name: string) {
  await userEvent.click(screen.getByRole('button', { name: /New Migration/ }));
  await userEvent.type(screen.getByLabelText(/Name \*/), name);
  await userEvent.click(screen.getByRole('button', { name: 'Apply Migration' }));
}

describe('MigrationsPage', () => {
  beforeEach(() => vi.clearAllMocks());

  test('lists the applied migrations', async () => {
    renderPage();
    expect(await screen.findByText('init')).toBeInTheDocument();
    expect(screen.getByText('first tables')).toBeInTheDocument();
    expect(screen.getByText('abcdef01')).toBeInTheDocument();
  });

  test("a refused apply shows the server's reason", async () => {
    vi.mocked(api.post).mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'version V2 was already applied' } },
    });
    renderPage();
    await screen.findByText('init');
    await applyNamed('add_orders');

    expect(await screen.findByText('version V2 was already applied')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/provision/p1/migrations', expect.objectContaining({ version: 'V2', name: 'add_orders' }));
  });

  test('a migration that ran and failed shows its error and keeps the form open', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { ...applied, status: 'FAILED', errorMessage: 'syntax error' } } as never);
    renderPage([]);
    await screen.findByText(/No migrations applied yet/);
    await applyNamed('broken');

    expect(await screen.findByText('syntax error')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Apply Migration' })).toBeInTheDocument();
  });

  test('a migration that applied closes the form and says how long it took', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { ...applied, version: 'V2' } } as never);
    renderPage();
    await screen.findByText('init');
    await applyNamed('add_orders');

    expect(await screen.findByText('V2 applied in 12ms')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Apply Migration' })).not.toBeInTheDocument();
  });
});
