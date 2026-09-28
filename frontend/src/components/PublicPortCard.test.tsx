import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PublicPortCard } from './PublicPortCard';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const closed = {
  projectId: 'p-1',
  publicEnabled: false,
  available: false,
  host: '',
  port: 0,
  requireTls: true,
  internal: {},
};
const open = {
  ...closed,
  publicEnabled: true,
  available: true,
  host: 'p-1.db.example.com',
  port: 30001,
};

function renderCard(endpoint: Record<string, unknown> | Error, status = 'ACTIVE') {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/projects/p-1/db-endpoint') {
      return endpoint instanceof Error
        ? Promise.reject(endpoint)
        : Promise.resolve({ data: endpoint } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <PublicPortCard projectId="p-1" status={status} />
    </QueryClientProvider>,
  );
}

describe('PublicPortCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('a closed port is presented as the deliberate private default and can be opened', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: open } as never);
    renderCard(closed);
    expect(await screen.findByTestId('public-port-state')).toHaveTextContent(/private/i);
    expect(screen.getByTestId('public-port-card')).toHaveTextContent(/default/i);
    await userEvent.click(screen.getByTestId('public-port-toggle'));
    await userEvent.click(screen.getByTestId('public-port-confirm'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/db-endpoint', { publicEnabled: true }),
    );
  });

  test('an open port shows its address and can be closed', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: closed } as never);
    renderCard(open);
    expect(await screen.findByTestId('public-port-state')).toHaveTextContent(
      'p-1.db.example.com:30001',
    );
    await userEvent.click(screen.getByTestId('public-port-toggle'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/db-endpoint', { publicEnabled: false }),
    );
  });

  test('shows the control plane reason when a change is refused', async () => {
    vi.mocked(api.put).mockRejectedValue({
      response: { status: 403, data: { error: 'admin role required' } },
    });
    renderCard(closed);
    await userEvent.click(await screen.findByTestId('public-port-toggle'));
    await userEvent.click(screen.getByTestId('public-port-confirm'));
    expect(await screen.findByRole('alert')).toHaveTextContent('admin role required');
  });

  test('says so when the installation offers no public ports', async () => {
    renderCard(
      Object.assign(new Error('503'), {
        response: { status: 503, data: { error: 'database endpoint is not configured' } },
      }),
    );
    expect(await screen.findByTestId('public-port-unavailable')).toBeInTheDocument();
    expect(screen.queryByTestId('public-port-toggle')).toBeNull();
  });

  test('a project that is not active cannot change its port', async () => {
    renderCard(closed, 'PAUSED');
    expect(await screen.findByTestId('public-port-toggle')).toBeDisabled();
    expect(screen.getByTestId('public-port-card')).toHaveTextContent(/must be active/i);
  });
});
