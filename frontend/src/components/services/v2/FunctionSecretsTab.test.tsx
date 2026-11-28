import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api } from '../../../api/client';
import { FunctionSecretsTab } from './FunctionSecretsTab';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

let secrets: unknown[] = [];

function route(url: string) {
  if (url.endsWith('/functions/secrets')) return Promise.resolve({ data: secrets });
  if (url.includes('egress')) return Promise.resolve({ data: { allowedHosts: [], defaultHosts: [] } });
  return Promise.reject(Object.assign(new Error('not mocked'), { response: { status: 404, data: {} } }));
}

function renderTab() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <FunctionSecretsTab projectId="p1" />
    </QueryClientProvider>,
  );
}

const SECRETS_URL = '/projects/p1/functions/secrets';

beforeEach(() => {
  vi.clearAllMocks();
  secrets = [];
  vi.mocked(api.get).mockImplementation(route as never);
  vi.mocked(api.post).mockResolvedValue({ data: { status: 'set' } } as never);
  vi.mocked(api.delete).mockResolvedValue({ data: { status: 'deleted' } } as never);
});

describe('FunctionSecretsTab', () => {
  test('lists key names only', async () => {
    secrets = [{ key: 'STRIPE_KEY' }];
    renderTab();
    expect(await screen.findByText('STRIPE_KEY')).toBeInTheDocument();
  });

  test('a read the server refuses is shown, not read as "No secrets set"', async () => {
    vi.mocked(api.get).mockImplementation(((url: string) =>
      url.endsWith('/functions/secrets')
        ? Promise.reject(Object.assign(new Error('x'), { response: { status: 503, data: { error: 'vault is sealed' } } }))
        : route(url)) as never);
    renderTab();
    expect(await screen.findByText(/vault is sealed/)).toBeInTheDocument();
    expect(screen.queryByText('No secrets set')).not.toBeInTheDocument();
  });

  test('saves a secret under an upper-cased key and shows the server warning', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'set', warning: 'saved, but not applied until verified' } } as never);
    renderTab();
    await userEvent.type(await screen.findByLabelText('Key'), 'api_base');
    await userEvent.type(screen.getByLabelText('Value'), 'https://x');
    await userEvent.click(screen.getByRole('button', { name: 'Save secret' }));
    expect(api.post).toHaveBeenCalledWith(SECRETS_URL, { key: 'API_BASE', value: 'https://x' });
    expect(await screen.findByRole('status')).toHaveTextContent('saved, but not applied until verified');
  });

  test('a double click on Save sends one request', async () => {
    let release: (value: unknown) => void = () => undefined;
    vi.mocked(api.post).mockReturnValue(new Promise((resolve) => { release = resolve; }) as never);
    renderTab();
    await userEvent.type(await screen.findByLabelText('Key'), 'A');
    await userEvent.type(screen.getByLabelText('Value'), 'v');
    const save = screen.getByRole('button', { name: 'Save secret' });
    // Both clicks land before React re-renders the button as disabled.
    await act(async () => {
      save.click();
      save.click();
    });
    expect(api.post).toHaveBeenCalledTimes(1);
    release({ data: { status: 'set' } });
  });

  test('deleting a secret asks first, then deletes', async () => {
    secrets = [{ key: 'STRIPE_KEY' }];
    renderTab();
    await userEvent.click(await screen.findByRole('button', { name: 'Delete STRIPE_KEY' }));
    expect(api.delete).not.toHaveBeenCalled();
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete' }));
    expect(api.delete).toHaveBeenCalledWith(`${SECRETS_URL}/STRIPE_KEY`);
  });

  test('a .env import saves every entry, in order', async () => {
    renderTab();
    await userEvent.click(await screen.findByLabelText('Paste .env (bulk import)'));
    await userEvent.paste('# note\nONE=1\nexport TWO="two words"\n');
    await userEvent.click(screen.getByRole('button', { name: 'Import from .env' }));
    expect(await screen.findByText('saved 2/2')).toBeInTheDocument();
    expect(vi.mocked(api.post).mock.calls).toEqual([[SECRETS_URL, { key: 'ONE', value: '1' }], [SECRETS_URL, { key: 'TWO', value: 'two words' }]]);
  });

  test('an entry the server refuses is named in the result', async () => {
    vi.mocked(api.post)
      .mockResolvedValueOnce({ data: { status: 'set' } } as never)
      .mockRejectedValueOnce(Object.assign(new Error('x'), { response: { status: 400, data: { error: 'bad key' } } }));
    renderTab();
    await userEvent.click(await screen.findByLabelText('Paste .env (bulk import)'));
    await userEvent.paste('ONE=1\nTWO=2');
    await userEvent.click(screen.getByRole('button', { name: 'Import from .env' }));
    expect(await screen.findByText(/saved 1\/2, 1 failed \(TWO: bad key\)/)).toBeInTheDocument();
  });
});
