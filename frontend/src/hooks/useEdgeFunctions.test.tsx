import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  useEdgeFunctions,
  useEdgeFunction,
  useCreateEdgeFunction,
  useDeleteEdgeFunction,
  useInvokeEdgeFunction,
  useRuntimeStatus,
  useEdgeFunctionLogs,
  useEdgeSecrets,
  useSetEdgeSecret,
  useDeleteEdgeSecret,
} from './useEdgeFunctions';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
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

beforeEach(() => vi.clearAllMocks());

describe('useEdgeFunctions hooks', () => {
  test('useEdgeFunctions GETs the function list', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [{ id: 'fn1' }] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useEdgeFunctions('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions');
  });

  test('useEdgeFunction GETs by id', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { id: 'fn1' } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useEdgeFunction('p1', 'fn1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions/fn1');
  });

  test('useCreateEdgeFunction POSTs the body', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { id: 'fn1' } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useCreateEdgeFunction('p1'), { wrapper: Wrapper });
    result.current.mutate({ id: 'fn1', name: 'F', files: [{ path: 'index.ts', content: 'x' }] });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/projects/p1/functions', expect.objectContaining({ id: 'fn1' }));
  });

  test('useDeleteEdgeFunction DELETEs the function', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeleteEdgeFunction('p1'), { wrapper: Wrapper });
    result.current.mutate('fn1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/projects/p1/functions/fn1');
  });

  test('useInvokeEdgeFunction POSTs body to /invoke', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: 'ok' } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useInvokeEdgeFunction('p1'), { wrapper: Wrapper });
    result.current.mutate({ fnId: 'fn1', body: '{"hello":"world"}' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith(
      '/projects/p1/functions/fn1/invoke',
      '{"hello":"world"}',
      expect.any(Object),
    );
  });

  test('useRuntimeStatus GETs runtime/status', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { status: 'healthy', healthy: true } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useRuntimeStatus('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions/runtime/status');
  });

  test('useEdgeFunctionLogs skips when fnId is null', async () => {
    const { Wrapper } = makeWrapper();
    renderHook(() => useEdgeFunctionLogs('p1', null), { wrapper: Wrapper });
    await new Promise((r) => setTimeout(r, 30));
    expect(api.get).not.toHaveBeenCalled();
  });

  test('useEdgeFunctionLogs GETs when fnId is set', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { logs: [] } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useEdgeFunctionLogs('p1', 'fn1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions/fn1/logs');
  });

  test('useEdgeSecrets GETs the secrets keys', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [{ key: 'API_KEY' }] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useEdgeSecrets('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions/secrets');
  });

  test('useSetEdgeSecret POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSetEdgeSecret('p1'), { wrapper: Wrapper });
    result.current.mutate({ key: 'API_KEY', value: 'xxx' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/projects/p1/functions/secrets', { key: 'API_KEY', value: 'xxx' });
  });

  test('useDeleteEdgeSecret DELETEs', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeleteEdgeSecret('p1'), { wrapper: Wrapper });
    result.current.mutate('API_KEY');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/projects/p1/functions/secrets/API_KEY');
  });
});
