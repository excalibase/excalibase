import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SlowQueriesPage } from './SlowQueriesPage';
import { api } from '../api/client';
import type { SlowQueryReport } from '../utils/slowQueries';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function report(overrides: Partial<SlowQueryReport> = {}): SlowQueryReport {
  return {
    engine: 'postgresql',
    enabled: true,
    sort: 'total',
    canReset: true,
    canEnable: true,
    queries: [
      { query: 'SELECT * FROM orders WHERE id = $1', calls: 1200, totalTimeMs: 3400, meanTimeMs: 2.8, rows: 1180 },
      { query: 'UPDATE stock SET n = n - $1', calls: 3, totalTimeMs: 900, meanTimeMs: 300, rows: 3 },
    ],
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/monitoring/slow-queries']}>
        <Routes>
          <Route path="/project/:projectId/monitoring/slow-queries" element={<SlowQueriesPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SlowQueriesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: report() } as never);
    vi.mocked(api.post).mockResolvedValue({ data: undefined } as never);
  });

  test('lists the statements with their cost, costliest in total first', async () => {
    renderPage();
    expect(await screen.findByText('SELECT * FROM orders WHERE id = $1')).toBeInTheDocument();
    expect(screen.getByText('3.40 s')).toBeInTheDocument();
    expect(screen.getByText('1,200')).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/provision/proj-1/slow-queries/?sort=total&limit=50');
  });

  test('sorting by mean asks the server again', async () => {
    renderPage();
    await screen.findByText('UPDATE stock SET n = n - $1');
    fireEvent.click(screen.getByRole('button', { name: 'Mean time' }));
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/proj-1/slow-queries/?sort=mean&limit=50'));
  });

  test('reset asks first, then clears the statistics', async () => {
    renderPage();
    await screen.findByText('UPDATE stock SET n = n - $1');
    fireEvent.click(screen.getByRole('button', { name: 'Reset statistics' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Reset' }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/proj-1/slow-queries/reset'));
  });

  test('a viewer reads the list but is not offered reset', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: report({ canReset: false, canEnable: false }) } as never);
    renderPage();
    await screen.findByText('UPDATE stock SET n = n - $1');
    expect(screen.queryByRole('button', { name: 'Reset statistics' })).not.toBeInTheDocument();
  });

  test('statistics that are off are explained, and an admin turns them on knowing Postgres restarts', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: report({ enabled: false, queries: [] }) } as never);
    renderPage();
    expect(await screen.findByText('Query statistics are off for this database')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Turn on query statistics' }));
    // The page says it, and so does the confirmation.
    await waitFor(() => expect(screen.getAllByText(/restarts once/)).toHaveLength(2));
    fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/proj-1/slow-queries/enable'));
  });

  test('a member who cannot turn them on is told who can', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: report({ enabled: false, queries: [], canEnable: false }) } as never);
    renderPage();
    expect(await screen.findByText(/An Admin of this project can turn them on/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Turn on query statistics' })).not.toBeInTheDocument();
  });

  test('a refused read shows the server reason', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { status: 409, data: { error: 'not available for MySQL projects' } } });
    renderPage();
    expect(await screen.findByText('not available for MySQL projects')).toBeInTheDocument();
  });
});
