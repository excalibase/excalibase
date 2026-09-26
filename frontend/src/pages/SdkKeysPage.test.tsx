import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SdkKeysPage } from './SdkKeysPage';
import { api } from '../api/client';

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
});
