import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useDeprovisionDatabase, usePauseProject, useResumeProject } from './useProvisioning';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const RESPOND_ASYNC = { headers: { Prefer: 'respond-async' } };

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return Wrapper;
}

// Successive reads of the project after the operation was accepted.
function projectReads(...reads: Array<Record<string, unknown>>) {
  for (const read of reads) {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { projectId: 'p1', ...read } } as never);
  }
}

beforeEach(() => vi.clearAllMocks());

// EXC-473: a pause takes a backup first, a resume waits for the database and a
// deletion stops the project before its grace period: minutes, longer than one
// request may take. Studio asks the server not to wait and follows the project.
describe('project lifecycle follows the project', () => {
  test('a pause is followed until the project is paused', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ status: 202, data: { projectId: 'p1', status: 'PAUSING' } } as never);
    projectReads({ status: 'PAUSING' }, { status: 'PAUSED' });
    const { result } = renderHook(() => usePauseProject(1), { wrapper: wrapper() });
    result.current.mutate({ projectId: 'p1' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/pause', { reason: 'manual' }, RESPOND_ASYNC);
    expect(api.get).toHaveBeenCalledWith('/provision/p1');
    expect(result.current.data?.status).toBe('PAUSED');
  });

  test('a pause the server could not complete fails with its reason', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ status: 202, data: { projectId: 'p1', status: 'PAUSING' } } as never);
    projectReads({
      status: 'ACTIVE',
      failureReason: 'pause cancelled: the pre-pause backup did not complete; the project is still running',
    });
    const { result } = renderHook(() => usePauseProject(1), { wrapper: wrapper() });
    result.current.mutate({ projectId: 'p1' });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect((result.current.error as Error).message).toMatch(/pre-pause backup did not complete/);
  });

  test('a resume is followed until the project runs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ status: 202, data: { projectId: 'p1', status: 'RESUMING' } } as never);
    projectReads({ status: 'RESUMING' }, { status: 'ACTIVE' });
    const { result } = renderHook(() => useResumeProject(1), { wrapper: wrapper() });
    result.current.mutate('p1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/provision/p1/resume', undefined, RESPOND_ASYNC);
  });

  test('a deletion is followed until the project is scheduled for deletion', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({ status: 202, data: { projectId: 'p1', status: 'PAUSING' } } as never);
    projectReads({ status: 'PAUSING' }, { status: 'PAUSED' }, { status: 'PENDING_DELETION' });
    const { result } = renderHook(() => useDeprovisionDatabase(1), { wrapper: wrapper() });
    result.current.mutate('p1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/provision/p1', RESPOND_ASYNC);
    expect(api.get).toHaveBeenCalledTimes(3);
  });

  test('a deletion answered at once is not followed', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({ status: 202, data: { projectId: 'p1', status: 'PENDING_DELETION' } } as never);
    const { result } = renderHook(() => useDeprovisionDatabase(1), { wrapper: wrapper() });
    result.current.mutate('p1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).not.toHaveBeenCalled();
  });

  test('a deletion whose stop failed fails with the reason', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({ status: 202, data: { projectId: 'p1', status: 'PAUSING' } } as never);
    projectReads({ status: 'PAUSING', failureReason: 'pause did not complete: the database was not confirmed stopped' });
    const { result } = renderHook(() => useDeprovisionDatabase(1), { wrapper: wrapper() });
    result.current.mutate('p1');
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect((result.current.error as Error).message).toMatch(/not confirmed stopped/);
  });
});
