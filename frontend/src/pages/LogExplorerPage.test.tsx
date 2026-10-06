import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { LogExplorerPage } from './LogExplorerPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const LOGS = ['INFO started', 'WARNING slow query', 'ERROR connection reset', ''].join('\n');

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/logs']}>
        <Routes>
          <Route path="/project/:projectId/logs" element={<LogExplorerPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('LogExplorerPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: LOGS } as never);
  });

  test('shows the log lines coloured by level', async () => {
    renderPage();
    expect(await screen.findByText('ERROR connection reset')).toHaveClass('text-red-400');
    expect(screen.getByText('WARNING slow query')).toHaveClass('text-yellow-400');
    expect(screen.getByText('INFO started')).toHaveClass('text-text-secondary');
    expect(api.get).toHaveBeenCalledWith('/provision/proj-1/logs?lines=50');
  });

  test('the search filters lines case-insensitively and says when nothing matches', async () => {
    renderPage();
    await screen.findByText('INFO started');
    fireEvent.change(screen.getByTestId('log-search-input'), { target: { value: 'error' } });
    expect(screen.getByText('ERROR connection reset')).toBeInTheDocument();
    expect(screen.queryByText('INFO started')).not.toBeInTheDocument();
    fireEvent.change(screen.getByTestId('log-search-input'), { target: { value: 'x'.repeat(500) } });
    expect(screen.getByText('No matching log lines')).toBeInTheDocument();
    fireEvent.change(screen.getByTestId('log-search-input'), { target: { value: '   ' } });
    expect(screen.getByText('INFO started')).toBeInTheDocument();
  });

  test('a wider range asks for more lines', async () => {
    renderPage();
    await screen.findByText('INFO started');
    fireEvent.click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/proj-1/logs?lines=500'));
  });

  test('says there are no logs when the database has written none', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: '' } as never);
    renderPage();
    expect(await screen.findByText('No logs available')).toBeInTheDocument();
  });

  test('a refused fetch shows the server reason instead of an empty log', async () => {
    vi.mocked(api.get).mockRejectedValue({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'the database is still starting', status: 409 } },
    });
    renderPage();
    expect(await screen.findByTestId('logs-error')).toHaveTextContent('the database is still starting');
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.queryByText('No logs available')).not.toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });
});
