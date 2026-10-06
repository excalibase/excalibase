import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { Toaster } from 'sonner';
import { ExtensionsPage } from './ExtensionsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const EXTENSIONS = [
  { name: 'pgcrypto', installedVersion: '1.3', defaultVersion: '1.3', schema: 'public', comment: 'crypto functions' },
  { name: 'vector', installedVersion: null, defaultVersion: '0.7', schema: null, comment: null },
];

const REFUSAL = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'extension "vector" is not allowed on this plan', status: 409 } },
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Toaster />
      <MemoryRouter initialEntries={['/project/proj-1/extensions']}>
        <Routes>
          <Route path="/project/:projectId/extensions" element={<ExtensionsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const pending = () => new Promise<never>(() => {});

describe('ExtensionsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: EXTENSIONS } as never);
  });

  test('lists installed and available extensions and filters by search', async () => {
    renderPage();
    expect(await screen.findByTestId('ext-pgcrypto')).toBeInTheDocument();
    expect(screen.getByTestId('ext-vector')).toHaveTextContent('No description');

    fireEvent.change(screen.getByTestId('ext-search'), { target: { value: 'crypto' } });
    expect(screen.queryByTestId('ext-vector')).not.toBeInTheDocument();
    expect(screen.getByTestId('ext-pgcrypto')).toBeInTheDocument();
  });

  test('enabling an extension the server refuses shows its reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(REFUSAL);
    renderPage();
    fireEvent.click(await screen.findByTestId('enable-ext-vector'));

    expect(await screen.findByText('extension "vector" is not allowed on this plan')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on Enable sends one request', async () => {
    vi.mocked(api.post).mockImplementation(pending);
    renderPage();
    const enable = await screen.findByTestId('enable-ext-vector');
    fireEvent.click(enable);
    fireEvent.click(enable);

    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
  });

  test('disabling sends one cascade drop even when confirmed twice', async () => {
    vi.mocked(api.delete).mockImplementation(pending);
    renderPage();
    fireEvent.click(await screen.findByTestId('disable-ext-pgcrypto'));
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);

    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/schema/proj-1/extensions/pgcrypto', { params: { cascade: true } }),
    );
    expect(api.delete).toHaveBeenCalledTimes(1);
  });

  test('a refused disable shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue({
      ...REFUSAL,
      response: { status: 409, data: { error: 'other objects depend on extension pgcrypto', status: 409 } },
    });
    renderPage();
    fireEvent.click(await screen.findByTestId('disable-ext-pgcrypto'));
    fireEvent.click(screen.getByTestId('modal-confirm'));

    expect(await screen.findByText('other objects depend on extension pgcrypto')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a successful disable closes the dialog', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    renderPage();
    fireEvent.click(await screen.findByTestId('disable-ext-pgcrypto'));
    fireEvent.click(screen.getByTestId('modal-confirm'));

    await waitFor(() => expect(screen.queryByTestId('confirm-modal')).not.toBeInTheDocument());
  });
});
