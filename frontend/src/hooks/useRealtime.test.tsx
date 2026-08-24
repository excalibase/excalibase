import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  useRealtimeTables,
  useEnableRealtimeTable,
  useDisableRealtimeTable,
  useEnableAllRealtime,
  useDisableAllRealtime,
} from './useRealtime';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: {
    get: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
    post: vi.fn(),
  },
}));

function withQueryClient(): { client: QueryClient; wrapper: React.FC<{ children: React.ReactNode }> } {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

describe('useRealtimeTables', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('GETs /projects/{id}/realtime/tables and returns the array', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: [
        { schema: 'public', table: 'posts', enabled: true },
        { schema: 'public', table: 'comments', enabled: false },
      ],
    } as never);

    const { wrapper } = withQueryClient();
    const { result } = renderHook(() => useRealtimeTables('proj-abc'), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/proj-abc/realtime/tables');
    expect(result.current.data).toEqual([
      { schema: 'public', table: 'posts', enabled: true },
      { schema: 'public', table: 'comments', enabled: false },
    ]);
  });

  test('skips fetch when projectId is empty', async () => {
    const { wrapper } = withQueryClient();
    renderHook(() => useRealtimeTables(''), { wrapper });
    await new Promise((r) => setTimeout(r, 30));
    expect(api.get).not.toHaveBeenCalled();
  });
});

describe('useEnableRealtimeTable', () => {
  beforeEach(() => vi.clearAllMocks());

  test('PUTs the per-table endpoint with schema/table in URL', async () => {
    vi.mocked(api.put).mockResolvedValueOnce({
      data: { schema: 'public', table: 'posts', enabled: true },
    } as never);

    const { wrapper } = withQueryClient();
    const { result } = renderHook(() => useEnableRealtimeTable('proj-abc'), { wrapper });
    result.current.mutate({ schema: 'public', table: 'posts' });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.put).toHaveBeenCalledWith('/projects/proj-abc/realtime/tables/public/posts');
  });

  test('invalidates the tables query so list refetches after enable', async () => {
    vi.mocked(api.put).mockResolvedValueOnce({
      data: { schema: 'public', table: 'posts', enabled: true },
    } as never);
    vi.mocked(api.get).mockResolvedValue({ data: [] } as never);

    const { client, wrapper } = withQueryClient();
    // Seed the cache so we can observe invalidation
    client.setQueryData(['realtime', 'proj-abc', 'tables'], []);

    const { result } = renderHook(() => useEnableRealtimeTable('proj-abc'), { wrapper });
    result.current.mutate({ schema: 'public', table: 'posts' });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const state = client.getQueryState(['realtime', 'proj-abc', 'tables']);
    expect(state?.isInvalidated).toBe(true);
  });
});

describe('useDisableRealtimeTable', () => {
  beforeEach(() => vi.clearAllMocks());

  test('DELETEs the per-table endpoint', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({
      data: { schema: 'nosql', table: 'notes', enabled: false },
    } as never);

    const { wrapper } = withQueryClient();
    const { result } = renderHook(() => useDisableRealtimeTable('proj-abc'), { wrapper });
    result.current.mutate({ schema: 'nosql', table: 'notes' });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/projects/proj-abc/realtime/tables/nosql/notes');
  });
});

describe('useEnableAllRealtime / useDisableAllRealtime', () => {
  beforeEach(() => vi.clearAllMocks());

  test('enable-all POSTs and returns added count', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { added: 3 } } as never);
    const { wrapper } = withQueryClient();
    const { result } = renderHook(() => useEnableAllRealtime('proj-abc'), { wrapper });
    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/projects/proj-abc/realtime/enable-all');
    expect(result.current.data).toEqual({ added: 3 });
  });

  test('disable-all POSTs and returns dropped count', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { dropped: 5 } } as never);
    const { wrapper } = withQueryClient();
    const { result } = renderHook(() => useDisableAllRealtime('proj-abc'), { wrapper });
    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/projects/proj-abc/realtime/disable-all');
    expect(result.current.data).toEqual({ dropped: 5 });
  });
});
