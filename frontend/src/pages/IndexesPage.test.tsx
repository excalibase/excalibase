import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { Toaster } from 'sonner';
import { IndexesPage } from './IndexesPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

function rejection(reason: string) {
  return {
    message: 'Request failed with status code 409',
    response: { status: 409, data: { error: reason, status: 409 } },
  };
}

const INDEXES = [
  { name: 'orders_pkey', columns: ['id'], unique: true, type: 'btree' },
  { name: 'orders_customer_idx', columns: ['customer_id', 'created_at'], unique: false, type: 'hash' },
];

function stubCatalog(indexes: unknown[] = INDEXES) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/schema/proj-1/tables') return Promise.resolve({ data: [{ name: 'orders', type: 'BASE TABLE' }] } as never);
    if (url === '/schema/proj-1/tables/orders/indexes') return Promise.resolve({ data: indexes } as never);
    if (url === '/schema/proj-1/tables/orders/columns') {
      return Promise.resolve({ data: [{ name: 'id', dataType: 'integer' }, { name: 'customer_id', dataType: 'integer' }] } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Toaster />
      <MemoryRouter initialEntries={['/project/proj-1/indexes']}>
        <Routes>
          <Route path="/project/:projectId/indexes" element={<IndexesPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function selectOrders() {
  await screen.findByRole('option', { name: 'orders' });
  fireEvent.change(screen.getByTestId('table-selector'), { target: { value: 'orders' } });
}

function panelSubmit() {
  const buttons = screen.getAllByRole('button', { name: /^(Create Index|Creating\.\.\.)$/ });
  return buttons[buttons.length - 1];
}

async function fillIndex(name: string) {
  await selectOrders();
  fireEvent.click(screen.getByTestId('create-index-btn'));
  fireEvent.change(screen.getByLabelText('Index Name'), { target: { value: name } });
  fireEvent.click(await screen.findByRole('checkbox', { name: /customer_id/ }));
}

describe('IndexesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubCatalog();
  });

  test('asks for a table, then lists its indexes', async () => {
    renderPage();
    expect(screen.getByText('Select a table to view its indexes')).toBeInTheDocument();
    await selectOrders();
    expect(await screen.findByText('orders_customer_idx')).toBeInTheDocument();
    expect(screen.getByText('customer_id, created_at')).toBeInTheDocument();
    expect(screen.getByText('yes')).toBeInTheDocument();
  });

  test('says so when the table has no index', async () => {
    stubCatalog([]);
    renderPage();
    await selectOrders();
    expect(await screen.findByText('No indexes on this table')).toBeInTheDocument();
  });

  test('an index needs a name, a table and at least one column', async () => {
    renderPage();
    await selectOrders();
    fireEvent.click(screen.getByTestId('create-index-btn'));
    expect(panelSubmit()).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Index Name'), { target: { value: '   ' } });
    const column = await screen.findByRole('checkbox', { name: /customer_id/ });
    fireEvent.click(column);
    expect(panelSubmit()).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Index Name'), { target: { value: 'idx' } });
    fireEvent.click(column);
    expect(panelSubmit()).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Table'), { target: { value: '' } });
    expect(panelSubmit()).toBeDisabled();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates a unique index of the chosen type', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderPage();
    await fillIndex(' orders_customer_uq  ');
    fireEvent.click(screen.getByRole('checkbox', { name: /Unique/ }));
    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'gin' } });
    fireEvent.click(panelSubmit());
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/proj-1/indexes', {
        schema: 'public', name: 'orders_customer_uq', table: 'orders', columns: ['customer_id'], unique: true, type: 'gin',
      }),
    );
    await waitFor(() => expect(screen.queryByLabelText('Index Name')).not.toBeInTheDocument());
  });

  test('a refused create shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(rejection('relation "orders_customer_idx" already exists'));
    renderPage();
    await fillIndex('orders_customer_idx');
    fireEvent.click(panelSubmit());
    expect(await screen.findByText('relation "orders_customer_idx" already exists')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on create sends one request', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    await fillIndex(' orders_customer_uq  ');
    const submit = panelSubmit();
    fireEvent.click(submit);
    fireEvent.click(submit);
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a refused drop shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue(rejection('cannot drop index orders_pkey because constraint requires it'));
    renderPage();
    await selectOrders();
    const row = (await screen.findByText('orders_pkey')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button'));
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByText('cannot drop index orders_pkey because constraint requires it')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on drop sends one request', async () => {
    vi.mocked(api.delete).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    await selectOrders();
    const row = (await screen.findByText('orders_customer_idx')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button'));
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(api.delete).toHaveBeenCalledTimes(1));
    expect(api.delete).toHaveBeenCalledTimes(1);
  });
});
