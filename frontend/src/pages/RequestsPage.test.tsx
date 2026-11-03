import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { RequestsPage } from './RequestsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

const body = {
  projectId: 'proj-1', range: '1h', from: '2026-10-25T09:00:00Z', to: '2026-10-25T10:00:00Z', stepSeconds: 60,
  points: [],
  totals: [
    { timestamp: '2026-10-25T09:00:00Z', family: 'graphql', requests: 1200, clientErrors: 12, serverErrors: 0, p50Ms: 8.2, p95Ms: 140, connections: 0, connectionsOpened: 0, messages: 0 },
  ],
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/monitoring/requests']}>
        <Routes>
          <Route path="/project/:projectId/monitoring/requests" element={<RequestsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('RequestsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: body } as never);
  });

  test('shows each family over the last hour by default', async () => {
    renderPage();
    expect(await screen.findByText('1,200')).toBeInTheDocument();
    expect(screen.getByText('1.0%')).toBeInTheDocument();
    expect(screen.getByText('140 ms')).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/provision/proj-1/requests?range=1h');
  });

  test('another range asks the server again', async () => {
    renderPage();
    await screen.findByText('1,200');
    fireEvent.click(screen.getByRole('button', { name: '7d' }));
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/proj-1/requests?range=7d'));
  });

  test('a failed read says so', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('boom'));
    renderPage();
    expect(await screen.findByText('Request metrics could not be loaded.')).toBeInTheDocument();
  });
});
