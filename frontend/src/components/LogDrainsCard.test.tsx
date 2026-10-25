import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { LogDrainsCard } from './LogDrainsCard';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import type { LogDrain } from '../api/logDrains';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const DRAINS_URL = '/projects/proj-1/log-drains';

const drainFixture = (overrides: Partial<LogDrain> = {}): LogDrain => ({
  id: 'ld_0123456789abcdef',
  name: 'datadog-prod',
  kind: 'datadog',
  datadogSite: 'us1',
  hasSecret: true,
  enabled: true,
  status: 'active',
  consecutiveFailures: 0,
  lastSuccessAt: '2026-10-25T10:00:00Z',
  sentLines: 1234,
  droppedLines: 5,
  createdAt: '2026-10-20T10:00:00Z',
  ...overrides,
});

const httpsDrain = drainFixture({
  id: 'ld_ffffffffffffffff',
  name: 'collector',
  kind: 'https',
  datadogSite: undefined,
  url: 'https://logs.example.com/...',
  headerName: 'Authorization',
});

const refusal = (status: number, error: string) =>
  Object.assign(new Error('x'), { response: { status, data: { error } } });

function renderCard({
  featureOn = true,
  orgRole = 'admin',
  drains = [drainFixture()] as LogDrain[] | Error,
}: { featureOn?: boolean; orgRole?: string; drains?: LogDrain[] | Error } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({
        data: { deploymentMode: 'cloud', features: { logdrains: featureOn } },
      } as never);
    if (url === '/provision/proj-1')
      return Promise.resolve({ data: { projectId: 'proj-1', orgId: 'o1' } } as never);
    if (url === '/orgs')
      return Promise.resolve({
        data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }],
      } as never);
    if (url === DRAINS_URL)
      return drains instanceof Error
        ? Promise.reject(drains)
        : Promise.resolve({ data: drains } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LogDrainsCard projectId="proj-1" />
    </QueryClientProvider>,
  );
}

// EXC-658: forwarding a project's logs to Datadog or an HTTPS endpoint.
describe('LogDrainsCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.patch).mockReset();
    vi.mocked(api.delete).mockReset();
    useAuthStore.setState({ user: { id: 'u1', username: 'u', role: 'user' } as never });
  });

  test('is hidden while the feature is off and asks nothing of the drains API', async () => {
    renderCard({ featureOn: false });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('log-drains')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(DRAINS_URL);
    expect(api.get).not.toHaveBeenCalledWith('/orgs');
  });

  test('is hidden from anyone below Admin', async () => {
    renderCard({ orgRole: 'developer' });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/orgs'));
    expect(screen.queryByTestId('log-drains')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(DRAINS_URL);
  });

  test('says quietly when the server refuses a non-Admin', async () => {
    renderCard({ drains: refusal(403, 'forbidden') });
    expect(
      await screen.findByText(/Only project Admins manage log forwarding/),
    ).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  test('titles itself as a beta and explains there is no backfill', async () => {
    renderCard();
    const card = await screen.findByTestId('log-drains');
    expect(card).toHaveTextContent('Log forwarding');
    expect(within(card).getByText('Beta')).toBeInTheDocument();
    expect(card).toHaveTextContent(/no backfill/i);
  });

  test('lists drains with destination, status, counts and the last error', async () => {
    renderCard({
      drains: [
        drainFixture(),
        drainFixture({
          id: 'ld_ffffffffffffffff',
          name: 'collector',
          kind: 'https',
          datadogSite: undefined,
          url: 'https://logs.example.com/...',
          status: 'failing',
          lastError: 'endpoint answered 503',
          consecutiveFailures: 4,
        }),
      ],
    });
    const datadog = await screen.findByTestId('log-drain-ld_0123456789abcdef');
    expect(datadog).toHaveTextContent('datadog-prod');
    expect(datadog).toHaveTextContent('Datadog US1');
    expect(datadog).toHaveTextContent('active');
    expect(datadog).toHaveTextContent(/1,?234 sent/);
    expect(datadog).toHaveTextContent('5 dropped');
    const https = screen.getByTestId('log-drain-ld_ffffffffffffffff');
    expect(https).toHaveTextContent('https://logs.example.com/...');
    expect(https).toHaveTextContent('failing');
    expect(https).toHaveTextContent('endpoint answered 503');
  });

  test('never shows a secret, only that one is stored', async () => {
    const user = userEvent.setup();
    const leaky = { ...drainFixture(), secret: 'dd-api-key-leak' } as LogDrain;
    renderCard({ drains: [leaky] });
    await user.click(await screen.findByTestId('log-drain-edit-ld_0123456789abcdef'));
    expect(screen.queryByText(/dd-api-key-leak/)).not.toBeInTheDocument();
    const secret = screen.getByTestId('log-drain-secret');
    expect(secret).toHaveAttribute('type', 'password');
    expect(secret).toHaveValue('');
    expect(secret).toHaveAttribute('placeholder', expect.stringMatching(/stored/));
  });

  test('an edit without a new secret keeps the stored one', async () => {
    const user = userEvent.setup();
    renderCard();
    vi.mocked(api.patch).mockResolvedValue({ data: drainFixture() } as never);
    await user.click(await screen.findByTestId('log-drain-edit-ld_0123456789abcdef'));
    const name = screen.getByTestId('log-drain-name');
    await user.clear(name);
    await user.type(name, 'renamed');
    await user.selectOptions(screen.getByTestId('log-drain-site'), 'eu1');
    await user.click(screen.getByTestId('log-drain-submit'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(`${DRAINS_URL}/ld_0123456789abcdef`, {
        name: 'renamed',
        datadogSite: 'eu1',
      }),
    );
  });

  test('editing an HTTPS drain leaves the URL blank and sends none when untouched', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [httpsDrain] });
    vi.mocked(api.patch).mockResolvedValue({ data: httpsDrain } as never);
    await user.click(await screen.findByTestId('log-drain-edit-ld_ffffffffffffffff'));
    const url = screen.getByTestId('log-drain-url');
    expect(url).toHaveValue('');
    expect(url).toHaveAttribute(
      'placeholder',
      expect.stringContaining('https://logs.example.com/...'),
    );
    await user.type(screen.getByTestId('log-drain-name'), '-2');
    await user.click(screen.getByTestId('log-drain-submit'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(`${DRAINS_URL}/ld_ffffffffffffffff`, {
        name: 'collector-2',
      }),
    );
  });

  test('a new host on a drain with a header waits for the header value', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [httpsDrain] });
    vi.mocked(api.patch).mockResolvedValue({ data: httpsDrain } as never);
    await user.click(await screen.findByTestId('log-drain-edit-ld_ffffffffffffffff'));
    await user.type(screen.getByTestId('log-drain-url'), 'https://other.example.net/in');
    expect(screen.getByTestId('log-drain-reenter')).toHaveTextContent(
      'Re-enter the header value to send it to a new URL or header',
    );
    expect(screen.getByTestId('log-drain-submit')).toBeDisabled();
    await user.type(screen.getByTestId('log-drain-secret'), 'Bearer new');
    expect(screen.queryByTestId('log-drain-reenter')).not.toBeInTheDocument();
    await user.click(screen.getByTestId('log-drain-submit'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(`${DRAINS_URL}/ld_ffffffffffffffff`, {
        name: 'collector',
        url: 'https://other.example.net/in',
        secret: 'Bearer new',
      }),
    );
  });

  test('a new path on the same host also needs the header value again', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [httpsDrain] });
    await user.click(await screen.findByTestId('log-drain-edit-ld_ffffffffffffffff'));
    await user.type(screen.getByTestId('log-drain-url'), 'https://logs.example.com/v2');
    expect(screen.getByTestId('log-drain-reenter')).toBeInTheDocument();
    expect(screen.getByTestId('log-drain-submit')).toBeDisabled();
  });

  test('clearing the header sends an empty header name and no secret', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [httpsDrain] });
    vi.mocked(api.patch).mockResolvedValue({ data: httpsDrain } as never);
    await user.click(await screen.findByTestId('log-drain-edit-ld_ffffffffffffffff'));
    await user.clear(screen.getByTestId('log-drain-header'));
    await user.click(screen.getByTestId('log-drain-submit'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(`${DRAINS_URL}/ld_ffffffffffffffff`, {
        name: 'collector',
        headerName: '',
      }),
    );
  });

  test('creates a Datadog drain and clears the key after', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [] });
    vi.mocked(api.post).mockResolvedValue({ data: drainFixture() } as never);
    await user.type(await screen.findByTestId('log-drain-name'), 'dd');
    await user.selectOptions(screen.getByTestId('log-drain-site'), 'us5');
    const secret = screen.getByTestId('log-drain-secret');
    expect(secret).toHaveAttribute('type', 'password');
    expect(secret).toHaveAttribute('autocomplete', 'off');
    await user.type(secret, 'dd-key');
    await user.click(screen.getByTestId('log-drain-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(DRAINS_URL, {
        name: 'dd',
        kind: 'datadog',
        datadogSite: 'us5',
        secret: 'dd-key',
      }),
    );
    await waitFor(() => expect(screen.getByTestId('log-drain-secret')).toHaveValue(''));
  });

  test('a Datadog drain needs an API key', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [] });
    await user.type(await screen.findByTestId('log-drain-name'), 'dd');
    await user.click(screen.getByTestId('log-drain-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/API key/);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates an HTTPS drain with a header', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [] });
    vi.mocked(api.post).mockResolvedValue({ data: drainFixture() } as never);
    await user.type(await screen.findByTestId('log-drain-name'), 'collector');
    await user.selectOptions(screen.getByTestId('log-drain-kind'), 'https');
    await user.type(screen.getByTestId('log-drain-url'), 'https://logs.example.com/in');
    await user.type(screen.getByTestId('log-drain-header'), 'Authorization');
    await user.type(screen.getByTestId('log-drain-secret'), 'Bearer abc');
    await user.click(screen.getByTestId('log-drain-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(DRAINS_URL, {
        name: 'collector',
        kind: 'https',
        url: 'https://logs.example.com/in',
        headerName: 'Authorization',
        secret: 'Bearer abc',
      }),
    );
  });

  test('refuses a non-HTTPS URL or another port before sending', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [] });
    await user.type(await screen.findByTestId('log-drain-name'), 'collector');
    await user.selectOptions(screen.getByTestId('log-drain-kind'), 'https');
    const url = screen.getByTestId('log-drain-url');
    await user.type(url, 'http://logs.example.com');
    await user.click(screen.getByTestId('log-drain-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/https:\/\//);
    await user.clear(url);
    await user.type(url, 'https://logs.example.com:8443/in');
    await user.click(screen.getByTestId('log-drain-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/443/);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('refuses a header name without a value', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [] });
    await user.type(await screen.findByTestId('log-drain-name'), 'collector');
    await user.selectOptions(screen.getByTestId('log-drain-kind'), 'https');
    await user.type(screen.getByTestId('log-drain-url'), 'https://logs.example.com');
    await user.type(screen.getByTestId('log-drain-header'), 'Authorization');
    await user.click(screen.getByTestId('log-drain-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/header name and a value/i);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('disabling a drain sends enabled false', async () => {
    const user = userEvent.setup();
    renderCard();
    vi.mocked(api.patch).mockResolvedValue({ data: drainFixture({ enabled: false }) } as never);
    await user.click(await screen.findByTestId('log-drain-toggle-ld_0123456789abcdef'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(`${DRAINS_URL}/ld_0123456789abcdef`, {
        enabled: false,
      }),
    );
  });

  test('enabling a disabled drain sends enabled true', async () => {
    const user = userEvent.setup();
    renderCard({
      drains: [
        drainFixture({ enabled: false, status: 'disabled', lastError: 'too many failures' }),
      ],
    });
    vi.mocked(api.patch).mockResolvedValue({ data: drainFixture() } as never);
    const row = await screen.findByTestId('log-drain-ld_0123456789abcdef');
    expect(row).toHaveTextContent('too many failures');
    await user.click(screen.getByTestId('log-drain-toggle-ld_0123456789abcdef'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(`${DRAINS_URL}/ld_0123456789abcdef`, {
        enabled: true,
      }),
    );
  });

  test('deletes a drain after asking', async () => {
    const user = userEvent.setup();
    renderCard();
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await user.click(await screen.findByTestId('log-drain-delete-ld_0123456789abcdef'));
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Delete drain' }));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith(`${DRAINS_URL}/ld_0123456789abcdef`),
    );
  });

  test('cannot add a fourth drain', async () => {
    renderCard({
      drains: [
        drainFixture({ id: 'ld_1' }),
        drainFixture({ id: 'ld_2' }),
        drainFixture({ id: 'ld_3' }),
      ],
    });
    expect(await screen.findByTestId('log-drain-submit')).toBeDisabled();
  });

  test('shows why the server refused a create', async () => {
    const user = userEvent.setup();
    renderCard({ drains: [] });
    vi.mocked(api.post).mockRejectedValue(
      refusal(409, 'log forwarding needs the platform log store'),
    );
    await user.type(await screen.findByTestId('log-drain-name'), 'dd');
    await user.type(screen.getByTestId('log-drain-secret'), 'dd-key');
    await user.click(screen.getByTestId('log-drain-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'log forwarding needs the platform log store',
    );
  });
});
