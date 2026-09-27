import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SdkKeysPage } from './SdkKeysPage';
import { api } from '../api/client';
import { displayPrefix, type SdkKey } from '../api/sdkKeys';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const listed = {
  keys: [
    { id: 7, keyPrefix: 'pk_prefix', keyType: 'publishable', name: 'web', createdAt: '2026-09-27T01:00:00Z' },
  ],
};

function renderPage() {
  vi.mocked(api.get).mockResolvedValue({ data: listed } as never);
  return renderRoute();
}

function renderRoute() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/api-keys']}>
        <Routes>
          <Route path="/project/:projectId/api-keys" element={<SdkKeysPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SDK keys', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
  });

  test('lists keys by prefix and date, never the key', async () => {
    renderPage();
    const row = await screen.findByTestId('sdk-key-7');
    expect(row).toHaveTextContent('web');
    expect(row).toHaveTextContent('esk_pub_live_pk_prefix');
    expect(row).toHaveTextContent('2026');
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/sdk-keys/');
  });

  test('a new secret key is shown once, with a server-only warning', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { id: 8, keyPrefix: 'xyz', keyType: 'secret', name: 'server', createdAt: '2026-09-27T02:00:00Z', plaintext: 'esk_sec_live_xyzFULL' },
    } as never);
    renderPage();
    await screen.findByTestId('sdk-key-7');

    await u.type(screen.getByLabelText('Key name'), 'server');
    await u.selectOptions(screen.getByLabelText('Key type'), 'secret');
    await u.click(screen.getByRole('button', { name: /generate key/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sdk-keys/', { name: 'server', keyType: 'secret' }));
    const shown = await screen.findByTestId('new-sdk-key');
    expect(shown).toHaveTextContent('esk_sec_live_xyzFULL');
    expect(within(shown).getByText(/server only/i)).toBeInTheDocument();

    await u.click(within(shown).getByRole('button', { name: /i have saved it/i }));
    expect(screen.queryByText('esk_sec_live_xyzFULL')).not.toBeInTheDocument();
  });

  test('a publishable key carries no server-only warning', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { id: 9, keyPrefix: 'pub', keyType: 'publishable', name: 'app', createdAt: '2026-09-27T02:00:00Z', plaintext: 'esk_pub_live_pubFULL' },
    } as never);
    renderPage();
    await screen.findByTestId('sdk-key-7');
    await u.click(screen.getByRole('button', { name: /generate key/i }));

    const shown = await screen.findByTestId('new-sdk-key');
    expect(shown).toHaveTextContent('esk_pub_live_pubFULL');
    expect(within(shown).queryByText(/server only/i)).not.toBeInTheDocument();
  });

  test('revoking asks first, then calls the API', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    renderPage();
    const row = await screen.findByTestId('sdk-key-7');

    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    expect(window.confirm).toHaveBeenCalled();
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/sdk-keys/7'));
  });

  test('a refused request is reported', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { data: { error: 'the auth service is unavailable; try again' } } });
    renderPage();
    await screen.findByTestId('sdk-key-7');
    await u.click(screen.getByRole('button', { name: /generate key/i }));
    expect(await screen.findByText('the auth service is unavailable; try again')).toBeInTheDocument();
  });

  test('the new key can be copied', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { id: 9, keyPrefix: 'pub', keyType: 'publishable', name: 'app', createdAt: '2026-09-27T02:00:00Z', plaintext: 'esk_pub_live_pubFULL' },
    } as never);
    renderPage();
    await screen.findByTestId('sdk-key-7');
    await u.click(screen.getByRole('button', { name: /generate key/i }));

    await u.click(within(await screen.findByTestId('new-sdk-key')).getByRole('button', { name: /copy key/i }));
    expect(await navigator.clipboard.readText()).toBe('esk_pub_live_pubFULL');
  });

  test('declining the confirmation keeps the key', async () => {
    const u = userEvent.setup();
    vi.mocked(window.confirm).mockReturnValue(false);
    renderPage();
    const row = await screen.findByTestId('sdk-key-7');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    expect(api.delete).not.toHaveBeenCalled();
  });

  test('a failed revoke is reported, with a fallback when auth gives no reason', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockRejectedValue(new Error('network'));
    renderPage();
    const row = await screen.findByTestId('sdk-key-7');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    expect(await screen.findByText('Could not revoke the key')).toBeInTheDocument();
  });

  test('a project without keys says so', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { keys: [] } } as never);
    renderRoute();
    expect(await screen.findByText('No keys yet.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  test('a failed listing is reported', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { data: { error: 'the auth service refused the request' } } });
    renderRoute();
    expect(await screen.findByText('the auth service refused the request')).toBeInTheDocument();
  });

  test('an unnamed key is labelled by its prefix when revoking', async () => {
    const u = userEvent.setup();
    vi.mocked(api.get).mockResolvedValue({
      data: { keys: [{ id: 3, keyPrefix: 'sk_prefix', keyType: 'secret', name: '', createdAt: '2026-09-27T01:00:00Z' }] },
    } as never);
    renderRoute();
    const row = await screen.findByTestId('sdk-key-3');
    expect(row).toHaveTextContent('—');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('esk_sec_live_sk_prefix'));
  });
});

describe('displayPrefix', () => {
  test('shows only the stored prefix for a type it does not know', () => {
    const key = { id: 1, keyPrefix: 'abc', keyType: 'legacy', name: '', createdAt: '' } as unknown as SdkKey;
    expect(displayPrefix(key)).toBe('abc');
  });
});
