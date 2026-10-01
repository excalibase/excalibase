import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { FunctionsPage } from './FunctionsPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import type { PermissionDocument } from '../api/permissions';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const fn = (name: string, volatility: string, argTypes = '', definition = 'SELECT 1') => ({
  name,
  schema: 'public',
  language: 'sql',
  returnType: 'SETOF notes',
  argTypes,
  volatility,
  definition,
});

const FUNCTIONS = [
  fn('search_notes', 'STABLE', 'q text, session jsonb'),
  fn('archive_note', 'VOLATILE', 'note_id integer', 'CREATE FUNCTION ... SECURITY DEFINER ...'),
  fn('count_all', 'STABLE'),
];

function doc(): PermissionDocument {
  return {
    projectId: 'p1',
    version: 1,
    tables: [],
    functions: [{ function: 'public.archive_note', exposedAs: 'MUTATION', inferPermissions: false, sessionArgument: null }],
    functionPermissions: [{ function: 'public.archive_note', role: 'editor' }],
  };
}

let current: PermissionDocument = doc();

function renderPage(orgRole = 'developer') {
  current = doc();
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1') return Promise.resolve({ data: { projectId: 'p1', orgId: 'o1' } } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }] } as never);
    if (url === '/provision/p1/permissions/') return Promise.resolve({ data: current } as never);
    if (url === '/schema/p1/functions') return Promise.resolve({ data: FUNCTIONS } as never);
    return Promise.reject(new Error('unexpected ' + url));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/database/functions']}>
        <Routes>
          <Route path="/project/:projectId/database/functions" element={<FunctionsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const access = (name: string) => screen.findByTestId(`fn-api-${name}`);

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.getState().setAuth({ id: 'u1', username: 'dev', email: 'd@x.test', role: 'user' });
  vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
  vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
  vi.mocked(api.delete).mockResolvedValue({} as never);
});

describe('FunctionsPage API tracking', () => {
  test('an untracked function is not in the API and can be tracked with its options', async () => {
    renderPage();
    const user = userEvent.setup();
    const card = await access('search_notes');
    expect(card).toHaveTextContent(/not in the api/i);
    await user.click(within(card).getByRole('button', { name: 'Track' }));
    expect(within(card).getByText(/exposed as a query/i)).toBeInTheDocument();
    const infer = within(card).getByLabelText('Infer permissions from select');
    expect(infer).toBeChecked();
    await user.click(infer);
    const session = within(card).getByLabelText('Session argument');
    expect(within(session).getAllByRole('option').map((o) => o.textContent)).toEqual(['none', 'session']);
    await user.selectOptions(session, 'session');

    const tracked = { function: 'public.search_notes', exposedAs: 'QUERY' as const, inferPermissions: false, sessionArgument: 'session' };
    vi.mocked(api.post).mockImplementationOnce(() => {
      current = { ...current, functions: [...current.functions, tracked] };
      return Promise.resolve({ data: { ...tracked, securityDefiner: true } } as never);
    });
    await user.click(within(card).getByRole('button', { name: 'Track function' }));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p1/tracked-functions/', {
        function: 'public.search_notes',
        inferPermissions: false,
        sessionArgument: 'session',
      }),
    );
    await waitFor(() => expect(card).toHaveTextContent(/runs with the owner's privileges/i));
    expect(card).toHaveTextContent(/session argument: session/i);
    expect(card).toHaveTextContent(/only the roles listed may call it/i);
  });

  test('a function without json arguments offers no session argument', async () => {
    renderPage();
    const user = userEvent.setup();
    const card = await access('count_all');
    await user.click(within(card).getByRole('button', { name: 'Track' }));
    expect(within(card).queryByLabelText('Session argument')).not.toBeInTheDocument();
    await user.click(within(card).getByRole('button', { name: 'Track function' }));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p1/tracked-functions/', {
        function: 'public.count_all',
        inferPermissions: true,
        sessionArgument: null,
      }),
    );
  });

  test('the server refusal is shown for an untrackable function', async () => {
    vi.mocked(api.post).mockRejectedValueOnce({
      response: { status: 400, data: { error: 'the function must return rows of a table or view (SETOF <table> or <table>)' } },
    });
    renderPage();
    const user = userEvent.setup();
    const card = await access('count_all');
    await user.click(within(card).getByRole('button', { name: 'Track' }));
    await user.click(within(card).getByRole('button', { name: 'Track function' }));
    expect(await within(card).findByRole('alert')).toHaveTextContent(/must return rows of a table or view/);
  });

  test('a tracked mutation shows how it is exposed, the definer warning and who may call it', async () => {
    renderPage();
    const card = await access('archive_note');
    await waitFor(() => expect(card).toHaveTextContent(/tracked · mutation/i));
    expect(card).toHaveTextContent(/security definer/i);
    expect(card).toHaveTextContent(/runs with the owner's privileges/i);
    expect(card).toHaveTextContent(/only the roles listed may call it/i);
    expect(card.textContent).not.toMatch(/as the caller/i);
    expect(within(card).getByText('editor')).toBeInTheDocument();
  });

  test('roles are added and removed on a tracked function', async () => {
    renderPage();
    const user = userEvent.setup();
    const card = await access('archive_note');
    const input = await within(card).findByLabelText('Role allowed to call archive_note');
    await user.type(input, 'service');
    await user.click(within(card).getByRole('button', { name: 'Allow role' }));
    expect(within(card).getByTestId('fn-role-error-archive_note')).toHaveTextContent(/bypasses permissions/);
    await user.clear(input);
    await user.type(input, 'manager');
    await user.click(within(card).getByRole('button', { name: 'Allow role' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p1/function-permissions/public.archive_note/roles/manager'),
    );
    await user.click(within(card).getByRole('button', { name: 'Remove editor from archive_note' }));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/provision/p1/function-permissions/public.archive_note/roles/editor'),
    );
  });

  test('a failed role change is shown', async () => {
    vi.mocked(api.put).mockRejectedValueOnce({ response: { status: 400, data: { error: 'function public.archive_note is not tracked' } } });
    renderPage();
    const user = userEvent.setup();
    const card = await access('archive_note');
    await user.type(await within(card).findByLabelText('Role allowed to call archive_note'), 'manager');
    await user.click(within(card).getByRole('button', { name: 'Allow role' }));
    expect(await within(card).findByTestId('fn-role-error-archive_note')).toHaveTextContent('is not tracked');
  });

  test('untrack confirms, then deletes', async () => {
    renderPage();
    const user = userEvent.setup();
    const card = await access('archive_note');
    await user.click(await within(card).findByRole('button', { name: 'Untrack' }));
    await user.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/provision/p1/tracked-functions/public.archive_note'));
  });

  test('a failed untrack is shown', async () => {
    vi.mocked(api.delete).mockRejectedValueOnce({ response: { status: 500, data: { error: 'the change could not be saved' } } });
    renderPage();
    const user = userEvent.setup();
    const card = await access('archive_note');
    await user.click(await within(card).findByRole('button', { name: 'Untrack' }));
    await user.click(screen.getByTestId('modal-confirm'));
    expect(await within(card).findByRole('alert')).toHaveTextContent('could not be saved');
  });

  test('a tracked query with inference explains who may call it', async () => {
    renderPage();
    const user = userEvent.setup();
    const card = await access('search_notes');
    await user.click(within(card).getByRole('button', { name: 'Track' }));
    const tracked = { function: 'public.search_notes', exposedAs: 'QUERY' as const, inferPermissions: true, sessionArgument: null };
    vi.mocked(api.post).mockImplementationOnce(() => {
      current = { ...current, functions: [...current.functions, tracked] };
      return Promise.resolve({ data: { ...tracked, securityDefiner: false } } as never);
    });
    await user.click(within(card).getByRole('button', { name: 'Track function' }));
    await waitFor(() => expect(card).toHaveTextContent(/tracked · query/i));
    expect(card).toHaveTextContent(/any role that can select the rows it returns \(SETOF notes\) may call it/i);
    expect(card).not.toHaveTextContent(/security definer/i);
  });

  test('a viewer sees no tracking controls and no permissions are fetched', async () => {
    renderPage('viewer');
    await screen.findByTestId('fn-search_notes');
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/orgs'));
    expect(screen.queryByRole('button', { name: 'Track' })).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/provision/p1/permissions/');
  });
});

describe('FunctionsPage database functions', () => {
  test('creates a function from the panel', async () => {
    renderPage();
    const user = userEvent.setup();
    await user.click(await screen.findByTestId('create-function-btn'));
    await user.type(screen.getByTestId('fn-name-input'), 'recent_notes');
    await user.clear(screen.getByLabelText('Returns'));
    await user.type(screen.getByLabelText('Returns'), 'SETOF notes');
    await user.type(screen.getByLabelText('Arguments'), 'n integer');
    await user.selectOptions(screen.getByLabelText('Language'), 'sql');
    await user.selectOptions(screen.getByLabelText('Volatility'), 'STABLE');
    await user.type(screen.getByTestId('fn-body-input'), 'SELECT * FROM notes LIMIT n');
    await user.click(screen.getByTestId('create-function-submit'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/p1/functions', expect.objectContaining({
        name: 'recent_notes',
        language: 'sql',
        returnType: 'SETOF notes',
        args: 'n integer',
        body: 'SELECT * FROM notes LIMIT n',
        volatility: 'STABLE',
      })),
    );
  });

  test('expands a definition and drops a function after confirming', async () => {
    renderPage();
    const user = userEvent.setup();
    const card = await screen.findByTestId('fn-count_all');
    await user.click(within(card).getByText('count_all'));
    expect(within(card).getByText('SELECT 1')).toBeInTheDocument();
    await user.click(within(card).getByRole('button', { name: 'Drop count_all' }));
    await user.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(api.delete).toHaveBeenCalled());
    expect(String(vi.mocked(api.delete).mock.calls[0][0])).toContain('/schema/p1/functions/count_all');
  });
});
