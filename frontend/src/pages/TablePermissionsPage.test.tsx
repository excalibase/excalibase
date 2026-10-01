import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TablePermissionsPage } from './TablePermissionsPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import type { PermissionDocument } from '../api/permissions';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn(), patch: vi.fn() },
}));

const column = (name: string, ordinalPosition: number) => ({
  name,
  dataType: 'text',
  nullable: true,
  primaryKey: name === 'id',
  unique: false,
  defaultValue: null,
  ordinalPosition,
  comment: null,
  characterMaximumLength: null,
  numericPrecision: null,
  numericScale: null,
});

const COLUMNS = [column('id', 1), column('owner_id', 2), column('total', 3)];

function doc(): PermissionDocument {
  return {
    projectId: 'p1',
    version: 4,
    tables: [
      { table: 'public.orders', role: 'anon', select: { filter: {}, columns: '*' } },
      {
        table: 'public.orders',
        role: 'user',
        select: { filter: { owner_id: { _eq: 'X-Excalibase-User-Id' } }, columns: ['id', 'total'], limit: 50 },
      },
      { table: 'public.orders', role: 'editor', update: { filter: {}, columns: '*' } },
      { table: 'public.customers', role: 'manager', select: { filter: {}, columns: '*' } },
    ],
    functions: [],
    functionPermissions: [],
  };
}

function renderPage(orgRole = 'developer') {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1') return Promise.resolve({ data: { projectId: 'p1', orgId: 'o1' } } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }] } as never);
    if (url === '/provision/p1/permissions/') return Promise.resolve({ data: doc() } as never);
    if (url === '/schema/p1/tables/orders/columns') return Promise.resolve({ data: COLUMNS } as never);
    return Promise.reject(new Error('unexpected ' + url));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/database/tables/public/orders/permissions']}>
        <Routes>
          <Route
            path="/project/:projectId/database/tables/:schema/:table/permissions"
            element={<TablePermissionsPage />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const cell = (role: string, op: string) => screen.findByTestId(`perm-cell-${role}-${op}`);

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.getState().setAuth({ id: 'u1', username: 'dev', email: 'd@x.test', role: 'user' });
  vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
  vi.mocked(api.delete).mockResolvedValue({} as never);
});

describe('TablePermissionsPage grid', () => {
  test('shows anon and user always, the table roles, and each cell state', async () => {
    renderPage();
    expect(await cell('anon', 'select')).toHaveTextContent('Full access');
    expect(await cell('user', 'select')).toHaveTextContent('Custom');
    expect(await cell('user', 'insert')).toHaveTextContent('No access');
    expect(await cell('editor', 'update')).toHaveTextContent('Full access');
    // A role with permissions on another table only is not a row here.
    expect(screen.queryByTestId('perm-cell-manager-select')).not.toBeInTheDocument();
  });

  test('explains service and invisibility', async () => {
    renderPage();
    const notes = await screen.findByTestId('permissions-notes');
    expect(notes).toHaveTextContent(/service \(secret API keys\) bypasses every permission/);
    expect(notes).toHaveTextContent(/no permission for a role does not exist for that role/);
  });

  test('a viewer is told permissions are for developers and nothing is fetched', async () => {
    renderPage('viewer');
    expect(await screen.findByText(/developers and above/i)).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/provision/p1/permissions/');
  });

  test('adding a role validates the name', async () => {
    renderPage();
    const user = userEvent.setup();
    const input = await screen.findByLabelText('New role');
    await user.type(input, 'service');
    await user.click(screen.getByRole('button', { name: 'Add role' }));
    expect(screen.getByTestId('add-role-error')).toHaveTextContent(/bypasses permissions/);

    await user.clear(input);
    await user.type(input, 'Bad-Name');
    await user.click(screen.getByRole('button', { name: 'Add role' }));
    expect(screen.getByTestId('add-role-error')).toHaveTextContent(/lower-case letters/);

    await user.clear(input);
    await user.type(input, 'editor');
    await user.click(screen.getByRole('button', { name: 'Add role' }));
    expect(screen.getByTestId('add-role-error')).toHaveTextContent(/already/);

    await user.clear(input);
    await user.type(input, 'manager');
    await user.click(screen.getByRole('button', { name: 'Add role' }));
    expect(await cell('manager', 'delete')).toHaveTextContent('No access');
  });
});

describe('TablePermissionsPage editor', () => {
  test('opens an existing select permission prefilled and saves the edit', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('user', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    expect(within(panel).getByLabelText('Row filter')).toHaveValue(
      JSON.stringify({ owner_id: { _eq: 'X-Excalibase-User-Id' } }, null, 2),
    );
    expect(within(panel).getByLabelText('id')).toBeChecked();
    expect(within(panel).getByLabelText('owner_id')).not.toBeChecked();
    expect(within(panel).getByLabelText('Row limit')).toHaveValue(50);

    await user.click(within(panel).getByLabelText('owner_id'));
    await user.clear(within(panel).getByLabelText('Row limit'));
    await user.click(within(panel).getByLabelText('Allow aggregations'));
    await user.click(within(panel).getByRole('button', { name: 'Save permission' }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/user/select', {
        filter: { owner_id: { _eq: 'X-Excalibase-User-Id' } },
        columns: ['id', 'owner_id', 'total'],
        allowAggregations: true,
      }),
    );
    await waitFor(() => expect(screen.queryByTestId('sidepanel')).not.toBeInTheDocument());
  });

  test('a new permission needs a row filter and a column; presets fill them', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('user', 'delete'));
    const panel = await screen.findByTestId('sidepanel');
    const save = within(panel).getByRole('button', { name: 'Save permission' });
    expect(within(panel).getByTestId('filter-error')).toHaveTextContent(/choose a row filter/i);
    expect(save).toBeDisabled();

    await user.selectOptions(within(panel).getByLabelText('Owner column for Row filter'), 'owner_id');
    await user.click(within(panel).getByRole('button', { name: 'Owner only' }));
    expect(within(panel).getByLabelText('Row filter')).toHaveValue(
      JSON.stringify({ owner_id: { _eq: 'X-Excalibase-User-Id' } }, null, 2),
    );
    await user.click(save);
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/user/delete', {
        filter: { owner_id: { _eq: 'X-Excalibase-User-Id' } },
      }),
    );
  });

  test('select needs at least one column', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('anon', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    expect(within(panel).getByLabelText('All columns')).toBeChecked();
    expect(within(panel).getByLabelText('id')).toBeDisabled();
    await user.click(within(panel).getByLabelText('All columns'));
    expect(within(panel).getByTestId('columns-error')).toHaveTextContent(/at least one column/);
    expect(within(panel).getByRole('button', { name: 'Save permission' })).toBeDisabled();
  });

  test('invalid expressions are refused before sending', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('anon', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    const filter = within(panel).getByLabelText('Row filter');
    await user.clear(filter);
    await user.type(filter, '{{"owner_id": {{"_eq": null}}');
    expect(within(panel).getByTestId('filter-error')).toHaveTextContent(/null is not comparable/);
    expect(within(panel).getByRole('button', { name: 'Save permission' })).toBeDisabled();

    await user.click(within(panel).getByRole('button', { name: 'Without any checks' }));
    expect(filter).toHaveValue('{}');
    expect(within(panel).queryByTestId('filter-error')).not.toBeInTheDocument();
  });

  test('insert: check, columns and presets are sent', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('user', 'insert'));
    const panel = await screen.findByTestId('sidepanel');
    await user.click(within(panel).getByRole('button', { name: 'Without any checks' }));
    await user.click(within(panel).getByLabelText('total'));
    await user.click(within(panel).getByRole('button', { name: 'Add preset' }));
    await user.selectOptions(within(panel).getByLabelText('Preset column 1'), 'owner_id');
    const value = within(panel).getByLabelText('Preset value 1');
    await user.clear(value);
    await user.type(value, 'X-Excalibase-User-Id');
    await user.click(within(panel).getByRole('button', { name: 'Save permission' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/user/insert', {
        check: {},
        columns: ['total'],
        set: { owner_id: 'X-Excalibase-User-Id' },
      }),
    );
  });

  test('presets are validated', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('user', 'insert'));
    const panel = await screen.findByTestId('sidepanel');
    await user.click(within(panel).getByRole('button', { name: 'Without any checks' }));
    await user.click(within(panel).getByLabelText('All columns'));
    await user.click(within(panel).getByRole('button', { name: 'Add preset' }));
    expect(within(panel).getByTestId('presets-error')).toHaveTextContent(/pick a column/i);
    await user.selectOptions(within(panel).getByLabelText('Preset column 1'), 'owner_id');
    const value = within(panel).getByLabelText('Preset value 1');
    await user.type(value, 'X-Excalibase-');
    expect(within(panel).getByTestId('presets-error')).toHaveTextContent(/not a valid session variable/);
    await user.click(within(panel).getByRole('button', { name: 'Add preset' }));
    await user.selectOptions(within(panel).getByLabelText('Preset column 2'), 'owner_id');
    expect(within(panel).getByTestId('presets-error')).toHaveTextContent(/twice/);
    await user.click(within(panel).getByRole('button', { name: 'Remove preset 2' }));
    await user.clear(value);
    await user.type(value, 'fixed');
    expect(within(panel).queryByTestId('presets-error')).not.toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: 'Save permission' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/user/insert', {
        check: {},
        columns: '*',
        set: { owner_id: 'fixed' },
      }),
    );
  });

  test('update: filter, check and columns are sent', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('editor', 'update'));
    const panel = await screen.findByTestId('sidepanel');
    expect(within(panel).getByLabelText('Row check')).toHaveValue('{}');
    const check = within(panel).getByLabelText('Row check');
    await user.clear(check);
    await user.type(check, '{{"total": {{"_gt": 0}}');
    await user.click(within(panel).getByRole('button', { name: 'Save permission' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/editor/update', {
        filter: {},
        check: { total: { _gt: 0 } },
        columns: '*',
      }),
    );
  });

  test('a server refusal is shown in the panel', async () => {
    vi.mocked(api.put).mockRejectedValueOnce({ response: { status: 400, data: { error: 'filter: bad column' } } });
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('anon', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    await user.click(within(panel).getByRole('button', { name: 'Save permission' }));
    expect(await within(panel).findByTestId('permission-save-error')).toHaveTextContent('filter: bad column');
    expect(screen.getByTestId('sidepanel')).toBeInTheDocument();
  });

  test('the row limit must be a positive whole number', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('anon', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    const limit = within(panel).getByLabelText('Row limit');
    await user.type(limit, '0');
    expect(within(panel).getByTestId('limit-error')).toHaveTextContent(/positive whole number/);
  });

  test('remove asks for confirmation, then deletes', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('anon', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    await user.click(within(panel).getByRole('button', { name: 'Remove permission' }));
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/anon/select'),
    );
  });

  test('a failed remove is shown', async () => {
    vi.mocked(api.delete).mockRejectedValueOnce({ response: { status: 500, data: { error: 'the change could not be saved' } } });
    renderPage();
    const user = userEvent.setup();
    await user.click(await cell('anon', 'select'));
    const panel = await screen.findByTestId('sidepanel');
    await user.click(within(panel).getByRole('button', { name: 'Remove permission' }));
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    expect(await within(panel).findByTestId('permission-save-error')).toHaveTextContent('could not be saved');
  });
});
