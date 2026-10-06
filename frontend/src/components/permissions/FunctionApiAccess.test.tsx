import { beforeEach, describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { FunctionApiAccess } from './FunctionApiAccess';
import { api } from '../../api/client';
import type { TrackedFunction } from '../../api/permissions';
import type { FunctionInfo } from '../../types/schema';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const REFUSED = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'function public.search is already tracked', status: 409 } },
};

const QUERY_FN: FunctionInfo = {
  name: 'search',
  schema: 'public',
  language: 'sql',
  returnType: 'SETOF posts',
  argTypes: 'term text, session jsonb',
  volatility: 'STABLE',
  definition: 'select 1',
};

const TRACKED: TrackedFunction = {
  function: 'public.search',
  exposedAs: 'QUERY',
  inferPermissions: true,
  sessionArgument: 'session',
};

function renderAccess(tracked: TrackedFunction | undefined, roles: string[] = [], fn: FunctionInfo = QUERY_FN) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <FunctionApiAccess projectId="proj-1" fn={fn} tracked={tracked} roles={roles} />
    </QueryClientProvider>,
  );
}

const roleField = () => screen.getByLabelText('Role allowed to call search');
const roleForm = () => roleField().closest('form') as HTMLFormElement;

describe('FunctionApiAccess: tracking', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('tracks a query function with the chosen session argument', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { ...TRACKED, securityDefiner: false } } as never);
    renderAccess(undefined);
    fireEvent.click(screen.getByRole('button', { name: 'Track' }));
    fireEvent.change(screen.getByLabelText('Session argument'), { target: { value: 'session' } });
    fireEvent.click(screen.getByLabelText('Infer permissions from select'));
    fireEvent.click(screen.getByRole('button', { name: 'Track function' }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/proj-1/tracked-functions/', {
        function: 'public.search',
        inferPermissions: false,
        sessionArgument: 'session',
      }),
    );
  });

  test('a volatile function is tracked as a mutation without inferred permissions', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { ...TRACKED, exposedAs: 'MUTATION', securityDefiner: true } } as never);
    renderAccess(undefined, [], { ...QUERY_FN, volatility: 'VOLATILE', argTypes: '' });
    fireEvent.click(screen.getByRole('button', { name: 'Track' }));
    expect(screen.getByText(/Exposed as a mutation/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Track function' }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/proj-1/tracked-functions/', {
        function: 'public.search',
        inferPermissions: false,
        sessionArgument: null,
      }),
    );
  });

  test("a refused track shows the server's reason, not the status code", async () => {
    vi.mocked(api.post).mockRejectedValue(REFUSED);
    renderAccess(undefined);
    fireEvent.click(screen.getByRole('button', { name: 'Track' }));
    fireEvent.click(screen.getByRole('button', { name: 'Track function' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('function public.search is already tracked');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('two quick clicks on track send one request', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}) as never);
    renderAccess(undefined);
    fireEvent.click(screen.getByRole('button', { name: 'Track' }));
    const track = screen.getByRole('button', { name: 'Track function' });
    fireEvent.click(track);
    fireEvent.click(track);

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('cancel closes the track form', () => {
    renderAccess(undefined);
    fireEvent.click(screen.getByRole('button', { name: 'Track' }));
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('button', { name: 'Track function' })).not.toBeInTheDocument();
  });
});

describe('FunctionApiAccess: tracked function', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('shows how it is exposed and warns about SECURITY DEFINER', () => {
    renderAccess(TRACKED, [], { ...QUERY_FN, definition: 'create function ... SECURITY DEFINER' });
    expect(screen.getByText(/Tracked · query/)).toBeInTheDocument();
    expect(screen.getByText(/Session argument: session/)).toBeInTheDocument();
    expect(screen.getByText(/SECURITY DEFINER: runs with the owner's privileges/)).toBeInTheDocument();
    expect(screen.getByText(/Any role that can select the rows it returns/)).toBeInTheDocument();
  });

  test('describes who may call a mutation and a non-inferred query', () => {
    renderAccess({ ...TRACKED, exposedAs: 'MUTATION' });
    expect(screen.getByText(/Only the roles listed may call it, and only if/)).toBeInTheDocument();
  });

  test('a non-inferred query is limited to the listed roles', () => {
    renderAccess({ ...TRACKED, inferPermissions: false });
    expect(screen.getByText(/^Only the roles listed may call it\./)).toBeInTheDocument();
  });

  test('an empty role is refused before sending', () => {
    renderAccess(TRACKED);
    fireEvent.submit(roleForm());
    expect(screen.getByTestId('fn-role-error-search')).toHaveTextContent('lower-case letters');
    expect(api.put).not.toHaveBeenCalled();
  });

  test('a role in the wrong format is refused before sending', () => {
    renderAccess(TRACKED);
    fireEvent.change(roleField(), { target: { value: 'Editor-1' } });
    fireEvent.submit(roleForm());
    expect(screen.getByTestId('fn-role-error-search')).toHaveTextContent('lower-case letters');
    expect(api.put).not.toHaveBeenCalled();
  });

  test('a role longer than 63 characters is refused before sending', () => {
    renderAccess(TRACKED);
    fireEvent.change(roleField(), { target: { value: `r${'a'.repeat(63)}` } });
    fireEvent.submit(roleForm());
    expect(screen.getByTestId('fn-role-error-search')).toHaveTextContent('at most 63 characters');
    expect(api.put).not.toHaveBeenCalled();
  });

  test('the service role and an already allowed role are refused', () => {
    renderAccess(TRACKED, ['editor']);
    fireEvent.change(roleField(), { target: { value: 'service' } });
    fireEvent.submit(roleForm());
    expect(screen.getByTestId('fn-role-error-search')).toHaveTextContent('service bypasses permissions');

    fireEvent.change(roleField(), { target: { value: 'editor' } });
    fireEvent.submit(roleForm());
    expect(screen.getByTestId('fn-role-error-search')).toHaveTextContent('editor is already allowed');
    expect(api.put).not.toHaveBeenCalled();
  });

  test('allows a role and clears the field', async () => {
    vi.mocked(api.put).mockResolvedValue({} as never);
    renderAccess(TRACKED);
    fireEvent.change(roleField(), { target: { value: 'editor' } });
    fireEvent.submit(roleForm());

    await waitFor(() => expect(roleField()).toHaveValue(''));
    expect(api.put).toHaveBeenCalledWith('/provision/proj-1/function-permissions/public.search/roles/editor');
  });

  test("a refused role shows the server's reason next to the field", async () => {
    vi.mocked(api.put).mockRejectedValue(REFUSED);
    renderAccess(TRACKED);
    fireEvent.change(roleField(), { target: { value: 'editor' } });
    fireEvent.submit(roleForm());

    expect(await screen.findByTestId('fn-role-error-search')).toHaveTextContent('function public.search is already tracked');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('two quick submits of a role send one request', async () => {
    vi.mocked(api.put).mockReturnValue(new Promise(() => {}) as never);
    renderAccess(TRACKED);
    fireEvent.change(roleField(), { target: { value: 'editor' } });
    fireEvent.submit(roleForm());
    fireEvent.submit(roleForm());

    await waitFor(() => expect(api.put).toHaveBeenCalled());
    expect(api.put).toHaveBeenCalledTimes(1);
  });

  test("a refused role removal shows the server's reason", async () => {
    vi.mocked(api.delete).mockRejectedValue(REFUSED);
    renderAccess(TRACKED, ['editor']);
    fireEvent.click(screen.getByRole('button', { name: 'Remove editor from search' }));

    expect(await screen.findByTestId('fn-role-error-search')).toHaveTextContent('function public.search is already tracked');
    expect(api.delete).toHaveBeenCalledWith('/provision/proj-1/function-permissions/public.search/roles/editor');
  });

  test("a refused untrack shows the server's reason", async () => {
    vi.mocked(api.delete).mockRejectedValue(REFUSED);
    renderAccess(TRACKED);
    fireEvent.click(screen.getByRole('button', { name: 'Untrack' }));
    fireEvent.click(screen.getByTestId('modal-confirm'));

    expect(await screen.findByRole('alert')).toHaveTextContent('function public.search is already tracked');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(api.delete).toHaveBeenCalledWith('/provision/proj-1/tracked-functions/public.search');
  });
});
