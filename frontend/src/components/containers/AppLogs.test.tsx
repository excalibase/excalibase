import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppLogs } from './AppLogs';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn() } }));

const BASE = '/projects/proj-1/apps/app-1/logs';

function renderLogs(pollIntervalMs = 10) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AppLogs projectId="proj-1" appId="app-1" pollIntervalMs={pollIntervalMs} />
    </QueryClientProvider>,
  );
}

describe('AppLogs', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('shows the lines and then only asks for newer ones', async () => {
    const pages = [
      {
        lines: [{ pod: 'web-1', time: '2026-09-27T10:00:00.1Z', text: 'listening on 8080' }],
        cursor: '2026-09-27T10:00:00.1Z',
      },
      {
        lines: [{ pod: 'web-1', time: '2026-09-27T10:00:01Z', text: 'GET / 200' }],
        cursor: '2026-09-27T10:00:01Z',
      },
    ];
    vi.mocked(api.get).mockImplementation(() =>
      Promise.resolve({
        data: pages.shift() ?? { lines: [], cursor: '2026-09-27T10:00:01Z' },
      } as never),
    );
    renderLogs();
    expect(await screen.findByText('listening on 8080')).toBeInTheDocument();
    expect(await screen.findByText('GET / 200')).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith(BASE, { params: {} });
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith(BASE, { params: { since: '2026-09-27T10:00:00.1Z' } }),
    );
    expect(screen.getAllByText('listening on 8080')).toHaveLength(1);
  });

  test('says so when there is nothing yet', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { lines: [] } } as never);
    renderLogs();
    expect(await screen.findByTestId('app-logs-empty')).toBeInTheDocument();
  });

  test('a refusal is shown', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { data: { error: 'not found' } } });
    renderLogs(60_000);
    expect(await screen.findByRole('alert')).toHaveTextContent('not found');
  });
});
