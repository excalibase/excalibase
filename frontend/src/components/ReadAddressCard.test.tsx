import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ReadAddressCard } from './ReadAddressCard';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const URL = '/projects/p-1/db-read-address';

const replicas = {
  projectId: 'p-1',
  replicas: 2,
  internal: {
    host: 'p-1-postgres-ro.ns.svc.cluster.local',
    port: 5432,
    connectionString:
      'postgresql://app@p-1-postgres-ro.ns.svc.cluster.local:5432/app?sslmode=require',
  },
  publicOffered: true,
  publicOpen: true,
  enabled: false,
  available: false,
  host: 'p-1.db.example.com',
  canChange: true,
};

function renderCard(
  view: Record<string, unknown> | Error,
  { featureOn = true, status = 'ACTIVE' } = {},
) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') {
      return Promise.resolve({
        data: { deploymentMode: 'cloud', features: { dbreadaddress: featureOn } },
      } as never);
    }
    if (url === URL) {
      return view instanceof Error
        ? Promise.reject(view)
        : Promise.resolve({ data: view } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <ReadAddressCard projectId="p-1" status={status} />
    </QueryClientProvider>,
  );
}

describe('ReadAddressCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('hidden and never asks the server while the flag is off', async () => {
    renderCard(replicas, { featureOn: false });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('read-address-card')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(URL);
  });

  test('shows the replicas, the internal address and what a replica read costs', async () => {
    renderCard(replicas);
    expect(await screen.findByTestId('read-address-replicas')).toHaveTextContent(
      '2 replicas serve reads',
    );
    expect(screen.getByTestId('read-address-internal')).toHaveTextContent(
      'p-1-postgres-ro.ns.svc.cluster.local:5432',
    );
    expect(screen.getByTestId('read-address-lag')).toHaveTextContent(/miss a write/);
    expect(screen.getByTestId('read-address-public')).toHaveTextContent('No public read port');
  });

  test('an admin opens the public read port', async () => {
    vi.mocked(api.put).mockResolvedValue({
      data: { ...replicas, enabled: true, available: true, port: 31002 },
    } as never);
    renderCard(replicas);
    await userEvent.click(await screen.findByTestId('read-address-toggle'));
    expect(api.put).toHaveBeenCalledWith(URL, { enabled: true });
    expect(await screen.findByTestId('read-address-public')).toHaveTextContent(
      'p-1.db.example.com:31002',
    );
    expect(screen.getByTestId('read-address-toggle')).toHaveTextContent('Close read port');
  });

  test('closing is offered on an open port', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: replicas } as never);
    renderCard({ ...replicas, enabled: true, available: false, port: 31002 });
    expect(await screen.findByTestId('read-address-public')).toHaveTextContent(
      '(not answering right now)',
    );
    await userEvent.click(screen.getByTestId('read-address-toggle'));
    expect(api.put).toHaveBeenCalledWith(URL, { enabled: false });
  });

  test('the read port waits for the public port', async () => {
    renderCard({ ...replicas, publicOpen: false });
    expect(await screen.findByTestId('read-address-needs-public')).toBeInTheDocument();
    expect(screen.queryByTestId('read-address-toggle')).not.toBeInTheDocument();
  });

  test('one copy has no read address', async () => {
    renderCard({ ...replicas, replicas: 0, reason: 'one-copy', internal: undefined });
    expect(await screen.findByTestId('read-address-none')).toHaveTextContent(/one copy/);
    expect(screen.queryByTestId('read-address-toggle')).not.toBeInTheDocument();
  });

  test('an open read port left with no replica says it refuses connections', async () => {
    renderCard({
      ...replicas,
      replicas: 0,
      reason: 'one-copy',
      internal: undefined,
      enabled: true,
      port: 31002,
    });
    expect(await screen.findByTestId('read-address-replicas')).toHaveTextContent(
      /refuses connections/,
    );
    expect(screen.getByTestId('read-address-toggle')).toHaveTextContent('Close read port');
  });

  test('a single host explains why there is none', async () => {
    renderCard({
      projectId: 'p-1',
      replicas: 0,
      reason: 'single-host',
      singleHost: true,
      publicOffered: false,
      publicOpen: false,
      enabled: false,
      available: false,
      canChange: false,
    });
    expect(await screen.findByTestId('read-address-none')).toHaveTextContent(
      /single host runs one copy/,
    );
  });

  test('a developer sees the address but no control', async () => {
    renderCard({ ...replicas, canChange: false });
    expect(await screen.findByTestId('read-address-admin-only')).toBeInTheDocument();
    expect(screen.queryByTestId('read-address-toggle')).not.toBeInTheDocument();
  });

  test('the control waits for an active project', async () => {
    renderCard(replicas, { status: 'PAUSED' });
    expect(await screen.findByTestId('read-address-toggle')).toBeDisabled();
  });

  test('an installation without public ports keeps the internal address', async () => {
    renderCard({ ...replicas, publicOffered: false, publicOpen: false });
    expect(await screen.findByTestId('read-address-internal')).toBeInTheDocument();
    expect(screen.getByText(/does not offer public database ports/)).toBeInTheDocument();
  });

  test('a refusal is shown', async () => {
    vi.mocked(api.put).mockRejectedValue({
      response: { status: 409, data: { error: 'a read address needs at least one replica' } },
    });
    renderCard(replicas);
    await userEvent.click(await screen.findByTestId('read-address-toggle'));
    expect(await screen.findByRole('alert')).toBeInTheDocument();
  });

  test('a read failure is shown', async () => {
    renderCard(
      Object.assign(new Error('boom'), { response: { status: 500, data: { error: 'down' } } }),
    );
    expect(await screen.findByTestId('read-address-error')).toBeInTheDocument();
  });
});
