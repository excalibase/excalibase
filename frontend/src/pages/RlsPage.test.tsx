import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { Toaster } from 'sonner';
import { RlsPage } from './RlsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

function rejection(reason: string) {
  return {
    message: 'Request failed with status code 409',
    response: { status: 409, data: { error: reason, status: 409 } },
  };
}

const TABLES = [
  { name: 'orders', type: 'BASE TABLE' },
  { name: 'order_totals', type: 'VIEW' },
];
const POLICIES = [
  { name: 'owners_read', table: 'orders', command: 'SELECT', roles: 'public', using: 'owner = 1', withCheck: null },
];

function stubCatalog(policies: unknown[] = POLICIES) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/schema/proj-1/policies') return Promise.resolve({ data: policies } as never);
    if (url === '/schema/proj-1/tables') return Promise.resolve({ data: TABLES } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Toaster />
      <MemoryRouter initialEntries={['/project/proj-1/rls']}>
        <Routes>
          <Route path="/project/:projectId/rls" element={<RlsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function fillPolicy(name: string) {
  fireEvent.click(await screen.findByTestId('create-policy-btn'));
  fireEvent.change(screen.getByTestId('policy-name-input'), { target: { value: name } });
  fireEvent.change(screen.getByLabelText('Table'), { target: { value: 'orders' } });
}

describe('RlsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubCatalog();
  });

  test('groups policies by table and offers RLS only on base tables', async () => {
    renderPage();
    expect(await screen.findByTestId('policy-row-owners_read')).toHaveTextContent('owner = 1');
    expect(screen.getByTestId('rls-toggle-orders')).toBeInTheDocument();
    expect(screen.queryByTestId('rls-toggle-order_totals')).not.toBeInTheDocument();
  });

  test('says so when no policy exists', async () => {
    stubCatalog([]);
    renderPage();
    expect(await screen.findByText('No policies defined yet')).toBeInTheDocument();
  });

  test('a policy needs a name and a table', async () => {
    renderPage();
    fireEvent.click(await screen.findByTestId('create-policy-btn'));
    const submit = screen.getByTestId('create-policy-submit');
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByTestId('policy-name-input'), { target: { value: '  ' } });
    fireEvent.change(screen.getByLabelText('Table'), { target: { value: 'orders' } });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByTestId('policy-name-input'), { target: { value: 'p1' } });
    fireEvent.change(screen.getByLabelText('Table'), { target: { value: '' } });
    expect(submit).toBeDisabled();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates a policy with its expressions', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderPage();
    await fillPolicy('owners_write');
    fireEvent.change(screen.getByLabelText('Command'), { target: { value: 'UPDATE' } });
    fireEvent.change(screen.getByLabelText('Roles'), { target: { value: 'reporting' } });
    fireEvent.change(screen.getByLabelText('USING expression'), { target: { value: 'owner = 1' } });
    fireEvent.change(screen.getByLabelText('WITH CHECK expression'), { target: { value: 'owner = 2' } });
    fireEvent.click(screen.getByTestId('create-policy-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/proj-1/policies', {
        schema: 'public', permissive: true, name: 'owners_write', table: 'orders',
        command: 'UPDATE', roles: 'reporting', using: 'owner = 1', withCheck: 'owner = 2',
      }),
    );
    await waitFor(() => expect(screen.queryByTestId('policy-name-input')).not.toBeInTheDocument());
  });

  test('a refused create shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(rejection('policy "owners_read" for table "orders" already exists'));
    renderPage();
    await fillPolicy('owners_read');
    fireEvent.click(screen.getByTestId('create-policy-submit'));
    expect(await screen.findByText('policy "owners_read" for table "orders" already exists')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on create sends one request', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    await fillPolicy('owners_write');
    const submit = screen.getByTestId('create-policy-submit');
    fireEvent.click(submit);
    fireEvent.click(submit);
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a refused RLS toggle shows the server reason', async () => {
    vi.mocked(api.patch).mockRejectedValue(rejection('must be owner of table orders'));
    renderPage();
    fireEvent.click(await screen.findByTestId('rls-toggle-orders'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/schema/proj-1/tables/orders', { rlsEnabled: true }),
    );
    expect(await screen.findByText('must be owner of table orders')).toBeInTheDocument();
  });

  test('a refused drop shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue(rejection('policy "owners_read" is in use'));
    renderPage();
    await screen.findByTestId('policy-row-owners_read');
    fireEvent.click(screen.getByTestId('policy-row-owners_read').querySelector('button')!);
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByText('policy "owners_read" is in use')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on drop sends one request', async () => {
    vi.mocked(api.delete).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    await screen.findByTestId('policy-row-owners_read');
    fireEvent.click(screen.getByTestId('policy-row-owners_read').querySelector('button')!);
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(api.delete).toHaveBeenCalledTimes(1));
    expect(api.delete).toHaveBeenCalledTimes(1);
  });
});
