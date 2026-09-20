import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  useInstances,
  useInstance,
  useCredentials,
  useProvisionDatabase,
  useDeprovisionDatabase,
  useConfigureBackup,
  useTriggerBackup,
  useListBackups,
  useRestoreFromBackup,
  useLogs,
} from './useProvisioning';
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

describe('useProvisioning hooks', () => {
  test('useInstances GETs all', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useInstances(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision');
  });

  test('useInstance GETs by id', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useInstance('proj-1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/proj-1');
  });

  test('useCredentials GETs creds', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { username: 'app' } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useCredentials('proj-1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/proj-1/credentials');
  });

  test('useProvisionDatabase POSTs to /provision', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { projectId: 'proj-1' } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useProvisionDatabase(), { wrapper: Wrapper });
    result.current.mutate({ projectName: 'p', orgId: 'o', databaseType: 'POSTGRESQL', tier: 'FREE', postgresVersion: '16' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/provision', expect.objectContaining({ projectName: 'p' }));
  });

  test('useDeprovisionDatabase DELETEs', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeprovisionDatabase(), { wrapper: Wrapper });
    result.current.mutate('proj-1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/provision/proj-1');
  });

  test('useConfigureBackup POSTs the schedule', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useConfigureBackup(), { wrapper: Wrapper });
    result.current.mutate({ projectId: 'p1', config: { schedule: '0 2 * * *', retention: 7 } });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/configure', expect.any(Object));
  });

  test('useTriggerBackup POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useTriggerBackup(), { wrapper: Wrapper });
    result.current.mutate('p1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/trigger');
  });

  test('useListBackups GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { backups: [], backupEnabled: false, schedule: '', retentionDays: 0 } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useListBackups('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalled();
  });

  test('useRestoreFromBackup POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useRestoreFromBackup('p1'), { wrapper: Wrapper });
    result.current.mutate({ newProjectName: 'restored-p', backupId: 'backup-1' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalled();
  });

  test('useLogs GETs with lines param', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: 'log line\n' } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useLogs('p1', 50), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/provision/p1/logs?lines=50');
  });
});
