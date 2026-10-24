import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MetricsEndpointCard } from './MetricsEndpointCard';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import type { MetricsKey } from '../api/metricsKeys';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const KEYS_URL = '/projects/proj-1/metrics-keys';

const keyFixture = (overrides: Partial<MetricsKey> = {}): MetricsKey => ({
  id: 'hash-1',
  name: 'grafana',
  tokenPrefix: 'excb_1a2b3c4',
  createdAt: '2026-10-15T10:00:00Z',
  expiresAt: '2027-01-13T10:00:00Z',
  ...overrides,
});

function renderCard({
  featureOn = true,
  orgRole = 'admin',
  keys = [keyFixture()],
}: { featureOn?: boolean; orgRole?: string; keys?: MetricsKey[] } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({
        data: { deploymentMode: 'cloud', features: { metricsendpoint: featureOn } },
      } as never);
    if (url === '/provision/proj-1')
      return Promise.resolve({ data: { projectId: 'proj-1', orgId: 'o1' } } as never);
    if (url === '/orgs')
      return Promise.resolve({
        data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }],
      } as never);
    if (url === KEYS_URL) return Promise.resolve({ data: keys } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MetricsEndpointCard projectId="proj-1" />
    </QueryClientProvider>,
  );
}

// EXC-657: a project's Prometheus endpoint and the keys that read it.
describe('MetricsEndpointCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.delete).mockReset();
    useAuthStore.setState({ user: { id: 'u1', username: 'u', role: 'user' } as never });
  });

  test('shows the endpoint URL and the keys by name and prefix', async () => {
    renderCard();
    const card = await screen.findByTestId('metrics-endpoint');
    expect(card).toHaveTextContent('/projects/proj-1/metrics');
    const row = await screen.findByTestId('metrics-key-hash-1');
    expect(row).toHaveTextContent('grafana');
    expect(row).toHaveTextContent('excb_1a2b3c4…');
    expect(row).toHaveTextContent('Never used');
  });

  test('is hidden while the feature is off', async () => {
    renderCard({ featureOn: false });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('metrics-endpoint')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(KEYS_URL);
    expect(api.get).not.toHaveBeenCalledWith('/orgs');
  });

  test('is hidden from anyone below Admin', async () => {
    renderCard({ orgRole: 'developer' });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/orgs'));
    expect(screen.queryByTestId('metrics-endpoint')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(KEYS_URL);
  });

  test('creates a key and shows it once, with a scrape example', async () => {
    const user = userEvent.setup();
    renderCard({ keys: [] });
    vi.mocked(api.post).mockResolvedValue({
      data: { ...keyFixture({ id: 'hash-9', name: 'prom' }), token: 'excb_the-secret' },
    } as never);
    await user.type(await screen.findByTestId('metrics-key-name'), 'prom');
    await user.click(screen.getByTestId('metrics-key-create'));
    expect(api.post).toHaveBeenCalledWith(KEYS_URL, { name: 'prom', expiresInDays: 90 });
    const issued = await screen.findByTestId('metrics-key-issued');
    expect(issued).toHaveTextContent('excb_the-secret');
    expect(issued).toHaveTextContent(/shown once/i);
    expect(issued).toHaveTextContent('credentials_file');
    await user.click(within(issued).getByRole('button', { name: /done/i }));
    expect(screen.queryByText('excb_the-secret')).not.toBeInTheDocument();
  });

  test('the expiry is chosen when the key is made', async () => {
    const user = userEvent.setup();
    renderCard({ keys: [] });
    vi.mocked(api.post).mockResolvedValue({
      data: { ...keyFixture(), token: 'excb_x' },
    } as never);
    await user.type(await screen.findByTestId('metrics-key-name'), 'prom');
    await user.selectOptions(screen.getByTestId('metrics-key-expiry'), '365');
    await user.click(screen.getByTestId('metrics-key-create'));
    expect(api.post).toHaveBeenCalledWith(KEYS_URL, { name: 'prom', expiresInDays: 365 });
  });

  test('needs a name before it creates', async () => {
    renderCard({ keys: [] });
    expect(await screen.findByTestId('metrics-key-create')).toBeDisabled();
  });

  test('shows why a create was refused', async () => {
    const user = userEvent.setup();
    renderCard({ keys: [] });
    vi.mocked(api.post).mockRejectedValue(
      Object.assign(new Error('x'), {
        response: {
          status: 409,
          data: { error: 'the project already holds as many metrics keys as it may' },
        },
      }),
    );
    await user.type(await screen.findByTestId('metrics-key-name'), 'prom');
    await user.click(screen.getByTestId('metrics-key-create'));
    expect(await screen.findByRole('alert')).toHaveTextContent('as many metrics keys');
  });

  test('revokes a key after asking', async () => {
    const user = userEvent.setup();
    renderCard();
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await user.click(await screen.findByTestId('metrics-key-revoke-hash-1'));
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Revoke key' }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${KEYS_URL}/hash-1`));
  });

  test('marks an expired key', async () => {
    renderCard({ keys: [keyFixture({ expiresAt: '2020-01-01T00:00:00Z' })] });
    expect(await screen.findByTestId('metrics-key-hash-1')).toHaveTextContent(/expired/i);
  });
});
