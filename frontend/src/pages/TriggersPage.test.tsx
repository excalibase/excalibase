import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { Toaster } from 'sonner';
import { TriggersPage } from './TriggersPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

function rejection(reason: string) {
  return {
    message: 'Request failed with status code 409',
    response: { status: 409, data: { error: reason, status: 409 } },
  };
}

const TRIGGERS = [
  { name: 'set_updated_at', table: 'orders', event: 'UPDATE', timing: 'BEFORE', function: 'touch', enabled: true },
  { name: 'audit_orders', table: 'orders', event: 'INSERT', timing: 'AFTER', function: 'audit', enabled: false },
];

function stubCatalog(triggers: unknown[] = TRIGGERS) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/schema/proj-1/triggers') return Promise.resolve({ data: triggers } as never);
    if (url === '/schema/proj-1/tables') return Promise.resolve({ data: [{ name: 'orders', type: 'BASE TABLE' }] } as never);
    if (url === '/schema/proj-1/functions') return Promise.resolve({ data: [{ name: 'touch' }] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Toaster />
      <MemoryRouter initialEntries={['/project/proj-1/triggers']}>
        <Routes>
          <Route path="/project/:projectId/triggers" element={<TriggersPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function fillTrigger(name: string) {
  fireEvent.click(await screen.findByTestId('create-trigger-btn'));
  fireEvent.change(screen.getByLabelText('Trigger Name'), { target: { value: name } });
  fireEvent.change(screen.getByLabelText('Table'), { target: { value: 'orders' } });
  await screen.findByRole('option', { name: 'touch' });
  fireEvent.change(screen.getByLabelText('Function'), { target: { value: 'touch' } });
}

function panelSubmit() {
  const buttons = screen.getAllByRole('button', { name: /^(Create Trigger|Creating\.\.\.)$/ });
  return buttons[buttons.length - 1];
}

describe('TriggersPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubCatalog();
  });

  test('groups triggers by table with their state', async () => {
    renderPage();
    expect(await screen.findByText('set_updated_at')).toBeInTheDocument();
    expect(screen.getByText('2')).toBeInTheDocument();
    expect(screen.getByText('enabled')).toBeInTheDocument();
    expect(screen.getByText('disabled')).toBeInTheDocument();
  });

  test('says so when no trigger exists', async () => {
    stubCatalog([]);
    renderPage();
    expect(await screen.findByText('No triggers found')).toBeInTheDocument();
  });

  test('a trigger needs a name, a table and a function', async () => {
    renderPage();
    fireEvent.click(await screen.findByTestId('create-trigger-btn'));
    expect(panelSubmit()).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Trigger Name'), { target: { value: '   ' } });
    fireEvent.change(screen.getByLabelText('Table'), { target: { value: 'orders' } });
    await screen.findByRole('option', { name: 'touch' });
    fireEvent.change(screen.getByLabelText('Function'), { target: { value: 'touch' } });
    expect(panelSubmit()).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Trigger Name'), { target: { value: 't1' } });
    fireEvent.change(screen.getByLabelText('Function'), { target: { value: '' } });
    expect(panelSubmit()).toBeDisabled();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates a trigger with its event and timing', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderPage();
    await fillTrigger('  orders_touch ');
    fireEvent.change(screen.getByLabelText('Event'), { target: { value: 'DELETE' } });
    fireEvent.change(screen.getByLabelText('Timing'), { target: { value: 'AFTER' } });
    fireEvent.click(panelSubmit());
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/proj-1/triggers', {
        schema: 'public', name: 'orders_touch', table: 'orders', event: 'DELETE', timing: 'AFTER', function: 'touch',
      }),
    );
    await waitFor(() => expect(screen.queryByLabelText('Trigger Name')).not.toBeInTheDocument());
  });

  test('a refused create shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(rejection('trigger "set_updated_at" for relation "orders" already exists'));
    renderPage();
    await fillTrigger('set_updated_at');
    fireEvent.click(panelSubmit());
    expect(await screen.findByText('trigger "set_updated_at" for relation "orders" already exists')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on create sends one request', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    await fillTrigger('  orders_touch ');
    const submit = panelSubmit();
    fireEvent.click(submit);
    fireEvent.click(submit);
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a refused drop shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue(rejection('trigger "set_updated_at" is required by the app'));
    renderPage();
    const row = (await screen.findByText('set_updated_at')).closest('tr')!;
    fireEvent.click(row.querySelector('button')!);
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByText('trigger "set_updated_at" is required by the app')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double click on drop sends one request', async () => {
    vi.mocked(api.delete).mockReturnValue(new Promise(() => {}) as never);
    renderPage();
    const row = (await screen.findByText('set_updated_at')).closest('tr')!;
    fireEvent.click(row.querySelector('button')!);
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(api.delete).toHaveBeenCalledTimes(1));
    expect(api.delete).toHaveBeenCalledTimes(1);
  });
});
