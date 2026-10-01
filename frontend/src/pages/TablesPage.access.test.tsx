import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TablesPage } from './TablesPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

// Who can reach each table through the API (EXC-370): a summary in the list,
// a link to the table's permission grid, and public read offered on create.

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn(), put: vi.fn() },
}));

const TABLES = [
  { name: 'orders', schema: 'public', type: 'BASE TABLE' },
  { name: 'customers', schema: 'public', type: 'BASE TABLE' },
];

const DOC = {
  projectId: 'p1',
  version: 2,
  tables: [
    { table: 'public.orders', role: 'user', select: { filter: {}, columns: '*' } },
    { table: 'public.orders', role: 'anon', select: { filter: {}, columns: '*' } },
    { table: 'public.orders', role: 'editor', update: { filter: {}, columns: '*' } },
  ],
  functions: [],
  functionPermissions: [],
};

function renderPage(orgRole = 'developer') {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1') return Promise.resolve({ data: { projectId: 'p1', orgId: 'o1' } } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }] } as never);
    if (url === '/provision/p1/permissions/') return Promise.resolve({ data: DOC } as never);
    if (url.endsWith('/tables')) return Promise.resolve({ data: TABLES } as never);
    return Promise.resolve({ data: [] } as never);
  });
  vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
  vi.mocked(api.put).mockResolvedValue({ data: {} } as never);

  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/database/tables']}>
        <Routes>
          <Route path="/project/:projectId/database/tables" element={<TablesPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function openCreatePanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByTestId('new-table-btn'));
  return screen.findByTestId('sidepanel');
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.getState().setAuth({ id: 'u1', username: 'dev', email: 'd@x.test', role: 'user' });
});

describe('TablesPage API access summary', () => {
  test('lists the roles that can read each table', async () => {
    renderPage();
    const orders = await screen.findByTestId('access-summary-orders');
    await waitFor(() => expect(orders).toHaveTextContent('anonuser'));
    expect(within(orders).getByText('anon')).toBeInTheDocument();
    expect(screen.getByTestId('access-summary-customers')).toHaveTextContent(/no api access/i);
  });

  test('links each table to its permissions', async () => {
    renderPage();
    const link = await screen.findByRole('link', { name: 'API permissions of orders' });
    expect(link).toHaveAttribute('href', '/project/p1/database/tables/public/orders/permissions');
  });

  test('a viewer gets no summary and the permissions are not fetched', async () => {
    renderPage('viewer');
    await screen.findByTestId('table-item-orders');
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/orgs'));
    expect(screen.queryByTestId('access-summary-orders')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/provision/p1/permissions/');
  });
});

describe('create table with public read', () => {
  test('both read options start unchecked and explain the default', async () => {
    renderPage();
    const user = userEvent.setup();
    const panel = await openCreatePanel(user);
    expect(await within(panel).findByLabelText('Anyone can read (anon)')).not.toBeChecked();
    expect(within(panel).getByLabelText('Signed-in users can read (user)')).not.toBeChecked();
    expect(within(panel).getByText(/nobody can reach it through the api/i)).toBeInTheDocument();
  });

  test('unchecked: the table is created and no permission is written', async () => {
    renderPage();
    const user = userEvent.setup();
    const panel = await openCreatePanel(user);
    await user.type(within(panel).getByTestId('table-name-input'), 'notes');
    await user.click(within(panel).getByTestId('create-table-submit'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/schema/p1/tables', expect.objectContaining({ name: 'notes' })));
    await waitFor(() => expect(screen.queryByTestId('sidepanel')).not.toBeInTheDocument());
    expect(api.put).not.toHaveBeenCalled();
  });

  test('checked: after the table exists, a select permission is written for each role', async () => {
    renderPage();
    const user = userEvent.setup();
    const panel = await openCreatePanel(user);
    await user.type(within(panel).getByTestId('table-name-input'), 'notes');
    await user.click(await within(panel).findByLabelText('Anyone can read (anon)'));
    await user.click(within(panel).getByLabelText('Signed-in users can read (user)'));
    await user.click(within(panel).getByTestId('create-table-submit'));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.notes/roles/anon/select', {
        filter: {},
        columns: '*',
      }),
    );
    expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.notes/roles/user/select', {
      filter: {},
      columns: '*',
    });
    const postOrder = vi.mocked(api.post).mock.invocationCallOrder[0];
    expect(vi.mocked(api.put).mock.invocationCallOrder[0]).toBeGreaterThan(postOrder);
    await waitFor(() => expect(screen.queryByTestId('sidepanel')).not.toBeInTheDocument());
  });

  test('a failed permission write is shown and the table is left', async () => {
    renderPage();
    vi.mocked(api.put).mockRejectedValueOnce({ response: { status: 400, data: { error: '"public.Notes" is not schema.name' } } });
    const user = userEvent.setup();
    const panel = await openCreatePanel(user);
    await user.type(within(panel).getByTestId('table-name-input'), 'Notes');
    await user.click(await within(panel).findByLabelText('Anyone can read (anon)'));
    await user.click(within(panel).getByTestId('create-table-submit'));

    const alert = await within(panel).findByRole('alert');
    expect(alert).toHaveTextContent(/Table Notes was created, but anon read could not be granted/);
    expect(alert).toHaveTextContent('is not schema.name');
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(within(panel).getByTestId('create-table-submit'));
    await waitFor(() => expect(screen.queryByTestId('sidepanel')).not.toBeInTheDocument());
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a viewer is not offered the read options', async () => {
    renderPage('viewer');
    const user = userEvent.setup();
    const panel = await openCreatePanel(user);
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/orgs'));
    expect(within(panel).queryByLabelText('Anyone can read (anon)')).not.toBeInTheDocument();
  });
});
