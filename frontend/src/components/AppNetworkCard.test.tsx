import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppNetworkCard } from './AppNetworkCard';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const off = { projectId: 'p-1', privateNetwork: false, applied: false, canChange: true };
const on = { ...off, privateNetwork: true, applied: true };

function renderCard(network: Record<string, unknown> | Error, status = 'ACTIVE') {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/projects/p-1/app-network') {
      return network instanceof Error ? Promise.reject(network) : Promise.resolve({ data: network } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AppNetworkCard projectId="p-1" status={status} />
    </QueryClientProvider>,
  );
}

describe('AppNetworkCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('off is the stated default and an admin can turn it on after confirming', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: on } as never);
    renderCard(off);
    expect(await screen.findByTestId('app-network-state')).toHaveTextContent(/off/i);
    expect(screen.getByTestId('app-network-card')).toHaveTextContent(/default/i);
    await userEvent.click(screen.getByTestId('app-network-toggle'));
    expect(api.put).not.toHaveBeenCalled();
    await userEvent.click(screen.getByTestId('app-network-confirm'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/app-network', { privateNetwork: true }),
    );
  });

  test('on shows how apps reach each other and turns off without a confirmation', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: off } as never);
    renderCard(on);
    expect(await screen.findByTestId('app-network-state')).toHaveTextContent(/on/i);
    expect(screen.getByTestId('app-network-card')).toHaveTextContent('http://<app name>');
    await userEvent.click(screen.getByTestId('app-network-toggle'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/app-network', { privateNetwork: false }),
    );
  });

  test('a setting the cluster does not hold is flagged', async () => {
    renderCard({ ...on, applied: false });
    expect(await screen.findByTestId('app-network-not-applied')).toBeInTheDocument();
  });

  test('only admins see the control', async () => {
    renderCard({ ...off, canChange: false });
    expect(await screen.findByTestId('app-network-admin-only')).toBeInTheDocument();
    expect(screen.queryByTestId('app-network-toggle')).not.toBeInTheDocument();
  });

  test('an inactive project cannot be turned on', async () => {
    renderCard(off, 'PAUSED');
    expect(await screen.findByTestId('app-network-toggle')).toBeDisabled();
  });

  test('shows the control plane reason when a change is refused', async () => {
    vi.mocked(api.put).mockRejectedValue({ response: { status: 409, data: { error: 'project is busy' } } });
    renderCard(off);
    await userEvent.click(await screen.findByTestId('app-network-toggle'));
    await userEvent.click(screen.getByTestId('app-network-confirm'));
    expect(await screen.findByRole('alert')).toHaveTextContent('project is busy');
  });
});
