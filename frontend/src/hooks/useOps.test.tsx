import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useCurrentMetrics, useMetricsHistory } from './useMetrics';
import { useMigrations, useApplyMigration } from './useMigrations';
import { usePerformanceSummary, useTopQueries, useWaitEvents, useEnablePerformanceInsights } from './usePerformance';
import { useSnapshots, useExportSnapshot, useDeleteSnapshot } from './useSnapshots';
import { useDeploymentMode, isSelfHosted, isCloud } from './useDeploymentMode';
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

describe('useMetrics', () => {
  test('useCurrentMetrics GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useCurrentMetrics('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/metrics/current');
  });

  test('useMetricsHistory passes limit', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useMetricsHistory('p1', 100), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/metrics/history?limit=100');
  });
});

describe('useMigrations', () => {
  test('useMigrations GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useMigrations('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/migrations');
  });

  test('useApplyMigration POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useApplyMigration('p1'), { wrapper: Wrapper });
    result.current.mutate({ version: '001', name: 'mig1', sql: 'SELECT 1' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalled();
  });
});

describe('usePerformance', () => {
  test('usePerformanceSummary', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => usePerformanceSummary('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/performance/summary');
  });

  test('useTopQueries with limit', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useTopQueries('p1', 25), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/performance/top-queries?limit=25');
  });

  test('useWaitEvents', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useWaitEvents('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/performance/wait-events');
  });

  test('useEnablePerformanceInsights POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useEnablePerformanceInsights('p1'), { wrapper: Wrapper });
    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/performance/enable');
  });
});

describe('useSnapshots', () => {
  test('useSnapshots', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSnapshots('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/snapshot');
  });

  test('useExportSnapshot', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useExportSnapshot('p1'), { wrapper: Wrapper });
    result.current.mutate({ format: 'custom' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalled();
  });

  test('useDeleteSnapshot', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeleteSnapshot('p1'), { wrapper: Wrapper });
    result.current.mutate('snap-1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/provision/p1/snapshot/snap-1');
  });
});

describe('useDeploymentMode', () => {
  test('returns mode from /config', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { deploymentMode: 'cloud' } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeploymentMode(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current).toBe('cloud'));
    expect(api.get).toHaveBeenCalledWith('/config');
  });

  test('isSelfHosted / isCloud helpers', () => {
    expect(isSelfHosted('selfhosted')).toBe(true);
    expect(isSelfHosted('cloud')).toBe(false);
    expect(isCloud('cloud')).toBe(true);
    expect(isCloud('selfhosted')).toBe(false);
  });
});
