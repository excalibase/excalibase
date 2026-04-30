import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  useAuthUsers,
  useUpdateAuthUser,
  useDeleteAuthUser,
  useAuthSessions,
} from './useAuthUsers';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), patch: vi.fn(), delete: vi.fn() },
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

describe('useAuthUsers', () => {
  beforeEach(() => vi.clearAllMocks());

  test('GETs /projects/:id/auth/users', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [{ id: 'u1' }] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useAuthUsers('proj-1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/auth/users');
  });

  test('useUpdateAuthUser PATCHes', async () => {
    vi.mocked(api.patch).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useUpdateAuthUser('proj-1'), { wrapper: Wrapper });
    result.current.mutate({ userId: 1, enabled: false });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.patch).toHaveBeenCalledWith('/projects/proj-1/auth/users/1', { enabled: false });
  });

  test('useDeleteAuthUser DELETEs', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeleteAuthUser('proj-1'), { wrapper: Wrapper });
    result.current.mutate(1);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/auth/users/1');
  });

  test('useAuthSessions GETs sessions', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useAuthSessions('proj-1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/auth/sessions');
  });
});
