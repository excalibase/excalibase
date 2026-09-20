import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useTableGrants, useSetTableExposed, isTableExposed, END_USER_ROLES } from './useTableGrants';
import type { TableGrantSet } from './useTableGrants';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, Wrapper };
}

function grantSet(partial: Partial<TableGrantSet> = {}): TableGrantSet {
  return { projectId: 'p1', enforced: true, grants: [], ...partial };
}

beforeEach(() => vi.clearAllMocks());

describe('useTableGrants', () => {
  test('reads the project exposure list', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: grantSet() } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useTableGrants('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/table-grants/');
  });

  test('does not fetch without a project', () => {
    const { Wrapper } = makeWrapper();
    renderHook(() => useTableGrants(''), { wrapper: Wrapper });
    expect(api.get).not.toHaveBeenCalled();
  });
});

describe('isTableExposed', () => {
  const exposed = grantSet({
    grants: [
      { id: 'g1', projectId: 'p1', resource: 'public.orders', operations: ['SELECT'], role: 'anon', enabled: true },
    ],
  });

  test('a table with an enabled grant is reachable', () => {
    expect(isTableExposed(exposed, 'public', 'orders')).toBe(true);
  });

  test('a table with no grant is not reachable', () => {
    expect(isTableExposed(exposed, 'public', 'customers')).toBe(false);
  });

  test('a disabled grant does not make a table reachable', () => {
    const off = grantSet({ grants: [{ ...exposed.grants[0], enabled: false }] });
    expect(isTableExposed(off, 'public', 'orders')).toBe(false);
  });

  test('an unqualified grant resource matches the table by bare name', () => {
    const bare = grantSet({ grants: [{ ...exposed.grants[0], resource: 'orders' }] });
    expect(isTableExposed(bare, 'public', 'orders')).toBe(true);
  });

  test('a grant on another schema does not match', () => {
    expect(isTableExposed(exposed, 'billing', 'orders')).toBe(false);
  });

  test('nothing loaded yet is not reachable', () => {
    expect(isTableExposed(undefined, 'public', 'orders')).toBe(false);
  });
});

describe('useSetTableExposed', () => {
  test('exposing a table grants both end-user roles', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: grantSet() } as never);
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSetTableExposed('p1'), { wrapper: Wrapper });

    result.current.mutate({ schema: 'public', table: 'orders', exposed: true });

    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(END_USER_ROLES.length));
    for (const role of END_USER_ROLES) {
      expect(api.post).toHaveBeenCalledWith('/provision/p1/table-grants/', {
        resource: 'public.orders',
        operations: ['SELECT', 'INSERT', 'UPDATE', 'DELETE'],
        role,
        enabled: true,
      });
    }
  });

  test('hiding a table deletes every grant on it, leaving other tables alone', async () => {
    const current = grantSet({
      grants: [
        { id: 'g-anon', projectId: 'p1', resource: 'public.orders', operations: ['SELECT'], role: 'anon', enabled: true },
        { id: 'g-auth', projectId: 'p1', resource: 'public.orders', operations: ['SELECT'], role: 'authenticated', enabled: true },
        { id: 'g-other', projectId: 'p1', resource: 'public.customers', operations: ['SELECT'], role: 'anon', enabled: true },
      ],
    });
    vi.mocked(api.get).mockResolvedValue({ data: current } as never);
    vi.mocked(api.delete).mockResolvedValue({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSetTableExposed('p1'), { wrapper: Wrapper });

    result.current.mutate({ schema: 'public', table: 'orders', exposed: false });

    await waitFor(() => expect(api.delete).toHaveBeenCalledTimes(2));
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/table-grants/g-anon');
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/table-grants/g-auth');
    expect(api.delete).not.toHaveBeenCalledWith('/provision/p1/table-grants/g-other');
  });

  test('exposing a table that already has one role grant adds only the missing one', async () => {
    const current = grantSet({
      grants: [
        { id: 'g-anon', projectId: 'p1', resource: 'public.orders', operations: ['SELECT'], role: 'anon', enabled: true },
      ],
    });
    vi.mocked(api.get).mockResolvedValue({ data: current } as never);
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    vi.mocked(api.patch).mockResolvedValue({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSetTableExposed('p1'), { wrapper: Wrapper });

    result.current.mutate({ schema: 'public', table: 'orders', exposed: true });

    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/table-grants/', expect.objectContaining({
      role: 'authenticated',
    }));
  });
});
