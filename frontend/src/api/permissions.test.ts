import { describe, test, expect, beforeEach, vi } from 'vitest';
import { api } from './client';
import {
  getPermissionDocument,
  putTablePermission,
  deleteTablePermission,
  trackFunction,
  untrackFunction,
  putFunctionPermission,
  deleteFunctionPermission,
  apiErrorMessage,
} from './permissions';

vi.mock('./client', () => ({
  api: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

describe('permissions api', () => {
  beforeEach(() => vi.clearAllMocks());

  test('reads the whole permission document', async () => {
    const doc = { projectId: 'p1', version: 3, tables: [], functions: [], functionPermissions: [] };
    vi.mocked(api.get).mockResolvedValue({ data: doc } as never);
    expect(await getPermissionDocument('p1')).toEqual(doc);
    expect(api.get).toHaveBeenCalledWith('/provision/p1/permissions/');
  });

  test('puts one table permission by table, role and operation', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { filter: {}, columns: '*' } } as never);
    await putTablePermission('p1', 'public.orders', 'anon', 'select', { filter: {}, columns: '*' });
    expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/anon/select', {
      filter: {},
      columns: '*',
    });
  });

  test('deletes one table permission', async () => {
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await deleteTablePermission('p1', 'public.orders', 'user', 'delete');
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.orders/roles/user/delete');
  });

  test('tracks a function and returns what the server decided', async () => {
    const answer = {
      function: 'public.search',
      exposedAs: 'QUERY',
      inferPermissions: true,
      sessionArgument: null,
      securityDefiner: true,
    };
    vi.mocked(api.post).mockResolvedValue({ data: answer } as never);
    expect(
      await trackFunction('p1', { function: 'public.search', inferPermissions: true, sessionArgument: null }),
    ).toEqual(answer);
    expect(api.post).toHaveBeenCalledWith('/provision/p1/tracked-functions/', {
      function: 'public.search',
      inferPermissions: true,
      sessionArgument: null,
    });
  });

  test('untracks a function', async () => {
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await untrackFunction('p1', 'public.search');
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/tracked-functions/public.search');
  });

  test('adds and removes a function permission', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await putFunctionPermission('p1', 'public.search', 'editor');
    await deleteFunctionPermission('p1', 'public.search', 'editor');
    expect(api.put).toHaveBeenCalledWith('/provision/p1/function-permissions/public.search/roles/editor');
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/function-permissions/public.search/roles/editor');
  });

  test('escapes path segments', async () => {
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await deleteTablePermission('p 1', 'public.a/b', 'user', 'select');
    expect(api.delete).toHaveBeenCalledWith('/provision/p%201/permissions/tables/public.a%2Fb/roles/user/select');
  });
});

describe('apiErrorMessage', () => {
  test('prefers the server message', () => {
    expect(apiErrorMessage({ response: { data: { error: 'filter: bad' } } }, 'fallback')).toBe('filter: bad');
  });

  test('falls back to the error message, then the fallback', () => {
    expect(apiErrorMessage(new Error('Network Error'), 'fallback')).toBe('Network Error');
    expect(apiErrorMessage(undefined, 'fallback')).toBe('fallback');
  });
});
