import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DatabaseAllowList } from './DatabaseAllowList';
import { PublicPortCard } from './PublicPortCard';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const SOURCES_URL = '/projects/p-1/db-endpoint/allowed-sources';

const listed = {
  projectId: 'p-1',
  access: 'listed',
  sources: ['203.0.113.0/24', '2001:db8::/32'],
  max: 20,
  publicEnabled: true,
  canChange: true,
  callerAddress: '198.51.100.23',
};

const closedEndpoint = {
  projectId: 'p-1',
  publicOffered: true,
  publicEnabled: false,
  available: false,
  host: '',
  port: 0,
  requireTls: true,
  internal: {},
  canChange: true,
};

function mockGets(sources: Record<string, unknown> | Error, featureOn = true, endpoint: Record<string, unknown> = closedEndpoint) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', features: { dballowlist: featureOn } } } as never);
    if (url === SOURCES_URL)
      return sources instanceof Error ? Promise.reject(sources) : Promise.resolve({ data: sources } as never);
    if (url === '/projects/p-1/db-endpoint') return Promise.resolve({ data: endpoint } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function wrap(node: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

describe('DatabaseAllowList', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('shows the listed sources and saves an edited list', async () => {
    mockGets(listed);
    vi.mocked(api.put).mockResolvedValue({ data: { ...listed, sources: ['2001:db8::/32', '192.0.2.0/24'] } } as never);
    wrap(<DatabaseAllowList projectId="p-1" />);
    const list = await screen.findByTestId('allow-list-sources');
    expect(list).toHaveTextContent('203.0.113.0/24');
    await userEvent.click(within(list).getByRole('button', { name: 'Remove 203.0.113.0/24' }));
    await userEvent.type(screen.getByLabelText('Address or range'), '192.0.2.0/24');
    await userEvent.click(screen.getByRole('button', { name: 'Add' }));
    await userEvent.click(screen.getByTestId('allow-list-save'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(SOURCES_URL, { sources: ['2001:db8::/32', '192.0.2.0/24'] }),
    );
  });

  test('adds the caller address as a single host', async () => {
    mockGets({ ...listed, sources: [] , access: 'unset', publicEnabled: false });
    vi.mocked(api.put).mockResolvedValue({ data: listed } as never);
    wrap(<DatabaseAllowList projectId="p-1" />);
    await userEvent.click(await screen.findByRole('button', { name: /add my address \(198\.51\.100\.23\)/i }));
    await userEvent.click(screen.getByTestId('allow-list-save'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SOURCES_URL, { sources: ['198.51.100.23/32'] }));
  });

  test('allowing any address is an explicit, warned choice', async () => {
    mockGets(listed);
    vi.mocked(api.put).mockResolvedValue({ data: { ...listed, access: 'any', sources: [] } } as never);
    wrap(<DatabaseAllowList projectId="p-1" />);
    await userEvent.click(await screen.findByLabelText(/allow any address/i));
    expect(screen.getByTestId('allow-list-any-warning')).toHaveTextContent(/anyone on the internet/i);
    await userEvent.click(screen.getByTestId('allow-list-save'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SOURCES_URL, { allowAny: true }));
  });

  test('an unset list on a closed port asks for a choice first', async () => {
    mockGets({ ...listed, access: 'unset', sources: [], publicEnabled: false });
    wrap(<DatabaseAllowList projectId="p-1" />);
    expect(await screen.findByTestId('allow-list-state')).toHaveTextContent(/choose who may connect/i);
  });

  test('a port opened before allow-lists says it admits any address', async () => {
    mockGets({ ...listed, access: 'unset', sources: [], publicEnabled: true });
    wrap(<DatabaseAllowList projectId="p-1" />);
    expect(await screen.findByTestId('allow-list-state')).toHaveTextContent(/any address.*before allow-lists/i);
  });

  test('someone below Admin sees the list but cannot change it', async () => {
    mockGets({ ...listed, canChange: false });
    wrap(<DatabaseAllowList projectId="p-1" />);
    expect(await screen.findByTestId('allow-list-sources')).toHaveTextContent('203.0.113.0/24');
    expect(screen.queryByTestId('allow-list-save')).toBeNull();
    expect(screen.queryByRole('button', { name: /remove/i })).toBeNull();
  });

  test('shows the server reason when a list is refused', async () => {
    mockGets(listed);
    vi.mocked(api.put).mockRejectedValue({ response: { status: 400, data: { error: 'invalid database allow-list: "203.0.113.5/24" has host bits set' } } });
    wrap(<DatabaseAllowList projectId="p-1" />);
    await userEvent.click(await screen.findByTestId('allow-list-save'));
    expect(await screen.findByRole('alert')).toHaveTextContent('host bits set');
  });

  test('a single host database is reachable from that host only', async () => {
    mockGets({ ...listed, access: 'host', sources: [], singleHost: true, canChange: false });
    wrap(<DatabaseAllowList projectId="p-1" />);
    expect(await screen.findByTestId('allow-list-state')).toHaveTextContent(/this host only/i);
    expect(screen.queryByTestId('allow-list-save')).toBeNull();
  });
});

describe('PublicPortCard with allow-lists', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('the port cannot be opened before who may connect is chosen', async () => {
    mockGets({ ...listed, access: 'unset', sources: [], publicEnabled: false });
    wrap(<PublicPortCard projectId="p-1" status="ACTIVE" />);
    expect(await screen.findByTestId('allow-list-state')).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId('public-port-toggle')).toBeDisabled());
    expect(screen.getByTestId('public-port-card')).toHaveTextContent(/choose who may connect/i);
  });

  test('with a list chosen the port opens and the warning names the list', async () => {
    mockGets({ ...listed, publicEnabled: false });
    vi.mocked(api.put).mockResolvedValue({ data: { ...closedEndpoint, publicEnabled: true } } as never);
    wrap(<PublicPortCard projectId="p-1" status="ACTIVE" />);
    await screen.findByTestId('allow-list-sources');
    await waitFor(() => expect(screen.getByTestId('public-port-toggle')).toBeEnabled());
    await userEvent.click(screen.getByTestId('public-port-toggle'));
    expect(screen.getByTestId('public-port-card')).toHaveTextContent(/only the addresses listed/i);
    await userEvent.click(screen.getByTestId('public-port-confirm'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p-1/db-endpoint', { publicEnabled: true }));
  });

  test('with the feature off nothing asks for the list', async () => {
    mockGets(listed, false);
    wrap(<PublicPortCard projectId="p-1" status="ACTIVE" />);
    await waitFor(() => expect(screen.getByTestId('public-port-toggle')).toBeEnabled());
    expect(api.get).not.toHaveBeenCalledWith(SOURCES_URL);
    expect(screen.queryByTestId('allow-list')).toBeNull();
  });
});
