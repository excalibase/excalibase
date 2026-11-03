import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { LogExplorerPage } from './LogExplorerPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

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
    // The server answers { logs: "..." }, not the bare text.
    vi.mocked(api.get).mockResolvedValue({ data: { logs: LOGS } } as never);
  });

  test('shows the log lines coloured by level', async () => {
    renderPage();
    expect(await screen.findByText('ERROR connection reset')).toHaveClass('text-red-400');
    expect(screen.getByText('WARNING slow query')).toHaveClass('text-yellow-400');
    expect(screen.getByText('INFO started')).toHaveClass('text-text-secondary');
    expect(api.get).toHaveBeenCalledWith('/provision/proj-1/logs?lines=50&since=1h');
  });

  test('the search filters lines case-insensitively and says when nothing matches', async () => {
    renderPage();
    await screen.findByText('INFO started');
    fireEvent.change(screen.getByTestId('log-search-input'), { target: { value: 'error' } });
    expect(screen.getByText('ERROR connection reset')).toBeInTheDocument();
    expect(screen.queryByText('INFO started')).not.toBeInTheDocument();
    fireEvent.change(screen.getByTestId('log-search-input'), {
      target: { value: 'x'.repeat(500) },
    });
    expect(screen.getByText('No matching log lines')).toBeInTheDocument();
    fireEvent.change(screen.getByTestId('log-search-input'), { target: { value: '   ' } });
    expect(screen.getByText('INFO started')).toBeInTheDocument();
  });

  test('a wider range searches further back and asks for more lines', async () => {
    renderPage();
    await screen.findByText('INFO started');
    fireEvent.click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith('/provision/proj-1/logs?lines=500&since=24h'),
    );
  });

  test('a search term is sent to the server once typing pauses, so stored logs are searched', async () => {
    renderPage();
    await screen.findByText('INFO started');
    fireEvent.change(screen.getByTestId('log-search-input'), { target: { value: 'connection' } });
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith('/provision/proj-1/logs?lines=50&since=1h&q=connection'),
    );
    // A term the server would refuse stays a filter of the loaded lines.
    vi.mocked(api.get).mockClear();
    fireEvent.change(screen.getByTestId('log-search-input'), {
      target: { value: 'x'.repeat(201) },
    });
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(api.get).not.toHaveBeenCalledWith(expect.stringContaining('q=xxx'));
  });

  test('says there are no logs when the database has written none', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { logs: '' } } as never);
    renderPage();
    expect(await screen.findByText('No logs available')).toBeInTheDocument();
  });

  test('a refused fetch shows the server reason instead of an empty log', async () => {
    vi.mocked(api.get).mockRejectedValue({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'the database is still starting', status: 409 } },
    });
    renderPage();
    expect(await screen.findByTestId('logs-error')).toHaveTextContent(
      'the database is still starting',
    );
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.queryByText('No logs available')).not.toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });
  // EXC-659: the plan decides how long logs are kept; Studio offers no range past it.
  test('says how long the plan keeps logs and offers no range past it', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { logs: LOGS, retentionHours: 24 } } as never);
    renderPage();
    expect(await screen.findByTestId('log-retention')).toHaveTextContent('Logs are kept for 1 day on this plan');
    expect(screen.getByRole('button', { name: '24h' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '7d' })).not.toBeInTheDocument();
  });

  test('a plan that keeps a week offers the week', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { logs: LOGS, retentionHours: 168 } } as never);
    renderPage();
    expect(await screen.findByTestId('log-retention')).toHaveTextContent('Logs are kept for 7 days on this plan');
    fireEvent.click(screen.getByRole('button', { name: '7d' }));
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/proj-1/logs?lines=1000&since=7d'));
  });

  test('a retention that is not whole days is said in hours', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { logs: LOGS, retentionHours: 36 } } as never);
    renderPage();
    expect(await screen.findByTestId('log-retention')).toHaveTextContent('Logs are kept for 36 hours on this plan');
  });

  test('without a plan retention every range is offered and nothing is said', async () => {
    renderPage();
    await screen.findByText('INFO started');
    expect(screen.getByRole('button', { name: '7d' })).toBeInTheDocument();
    expect(screen.queryByTestId('log-retention')).not.toBeInTheDocument();
  });

  test('shows log forwarding to an Admin when the logdrains feature is on', async () => {
    useAuthStore.setState({ user: { id: 'u1', username: 'u', role: 'platform_admin' } as never });
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === '/config')
        return Promise.resolve({
          data: { deploymentMode: 'cloud', features: { logdrains: true } },
        } as never);
      if (url === '/projects/proj-1/log-drains') return Promise.resolve({ data: [] } as never);
      return Promise.resolve({ data: { logs: LOGS } } as never);
    });
    renderPage();
    expect(await screen.findByTestId('log-drains')).toHaveTextContent('Log forwarding');
    useAuthStore.setState({ user: null });
  });

  test('has no log forwarding while the feature is off', async () => {
    renderPage();
    await screen.findByText('INFO started');
    expect(screen.queryByTestId('log-drains')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/log-drains');
  });
});
