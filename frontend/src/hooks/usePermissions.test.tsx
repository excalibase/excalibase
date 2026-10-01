import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  usePermissionDocument,
  useSaveTablePermission,
  useDeleteTablePermission,
  useTrackFunction,
  useUntrackFunction,
  useAddFunctionPermission,
  useRemoveFunctionPermission,
} from './usePermissions';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, Wrapper };
}

const DOC = { projectId: 'p1', version: 1, tables: [], functions: [], functionPermissions: [] };

describe('usePermissions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: DOC } as never);
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    vi.mocked(api.delete).mockResolvedValue({} as never);
  });

  test('reads the document only when enabled', async () => {
    const { Wrapper } = makeWrapper();
    const off = renderHook(() => usePermissionDocument('p1', false), { wrapper: Wrapper });
    expect(off.result.current.fetchStatus).toBe('idle');
    expect(api.get).not.toHaveBeenCalled();

    const on = renderHook(() => usePermissionDocument('p1', true), { wrapper: Wrapper });
    await waitFor(() => expect(on.result.current.data).toEqual(DOC));
  });

  test('every write refreshes the document', async () => {
    const { client, Wrapper } = makeWrapper();
    const invalidate = vi.spyOn(client, 'invalidateQueries');
    const hooks = renderHook(
      () => ({
        save: useSaveTablePermission('p1'),
        remove: useDeleteTablePermission('p1'),
        track: useTrackFunction('p1'),
        untrack: useUntrackFunction('p1'),
        add: useAddFunctionPermission('p1'),
        drop: useRemoveFunctionPermission('p1'),
      }),
      { wrapper: Wrapper },
    );
    await act(async () => {
      await hooks.result.current.save.mutateAsync({
        table: 'public.t',
        role: 'user',
        operation: 'delete',
        permission: { filter: {} },
      });
      await hooks.result.current.remove.mutateAsync({ table: 'public.t', role: 'user', operation: 'delete' });
      await hooks.result.current.track.mutateAsync({ function: 'public.f', inferPermissions: true, sessionArgument: null });
      await hooks.result.current.untrack.mutateAsync('public.f');
      await hooks.result.current.add.mutateAsync({ function: 'public.f', role: 'editor' });
      await hooks.result.current.drop.mutateAsync({ function: 'public.f', role: 'editor' });
    });
    expect(api.put).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.t/roles/user/delete', { filter: {} });
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/permissions/tables/public.t/roles/user/delete');
    expect(api.post).toHaveBeenCalledWith('/provision/p1/tracked-functions/', {
      function: 'public.f',
      inferPermissions: true,
      sessionArgument: null,
    });
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/tracked-functions/public.f');
    expect(api.put).toHaveBeenCalledWith('/provision/p1/function-permissions/public.f/roles/editor');
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/function-permissions/public.f/roles/editor');
    expect(invalidate).toHaveBeenCalledTimes(6);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['permissions', 'p1'] });
  });
});
