import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useCredentialsWhenReady, CREDENTIALS_POLL_CAP_MS } from './useCredentialsWhenReady';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

const CREDENTIALS = { projectId: 'p-1', databaseName: 'app', username: 'app', sslMode: 'require', port: 5432 };
const failing = (status: number) => Promise.reject({ response: { status } });

function mount(status?: string) {
  const client = new QueryClient();
  const Wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return renderHook(({ s }) => useCredentialsWhenReady('p-1', s), { wrapper: Wrapper, initialProps: { s: status } });
}

const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const calls = () => vi.mocked(api.get).mock.calls.length;

beforeEach(() => { vi.useFakeTimers(); vi.clearAllMocks(); });
afterEach(() => vi.useRealTimers());

describe('useCredentialsWhenReady', () => {
  test('503 then 200 fills in', async () => {
    let n = 0;
    vi.mocked(api.get).mockImplementation((() => (++n < 3 ? failing(503) : Promise.resolve({ data: CREDENTIALS }))) as never);
    const { result } = mount();
    await advance(0);
    expect(result.current.credentials).toBeUndefined();
    expect(result.current.gaveUp).toBe(false);
    await advance(30_000);
    expect(result.current.credentials?.username).toBe('app');
    const settled = calls();
    await advance(60_000);
    expect(calls()).toBe(settled);
  });

  test('503 backs off from 3 s to at most 15 s', async () => {
    vi.mocked(api.get).mockImplementation((() => failing(503)) as never);
    mount();
    await advance(0);
    await advance(10 * 60_000);
    // A flat 3 s poll would make about 200 calls in ten minutes.
    expect(calls()).toBeGreaterThan(30);
    expect(calls()).toBeLessThan(60);
  });

  test.each([403, 404, 409])('%i stops polling and gives up', async (status) => {
    vi.mocked(api.get).mockImplementation((() => failing(status)) as never);
    const { result } = mount();
    await advance(0);
    const first = calls();
    await advance(5 * 60_000);
    expect(calls()).toBe(first);
    expect(result.current.gaveUp).toBe(true);
  });

  test.each(['FAILED', 'DELETING', 'PENDING_DELETION', 'BACKUPS_PENDING_DELETE'])(
    'a %s project is not polled',
    async (status) => {
      vi.mocked(api.get).mockImplementation((() => failing(503)) as never);
      const { result } = mount(status);
      await advance(5 * 60_000);
      expect(calls()).toBeLessThanOrEqual(1);
      expect(result.current.gaveUp).toBe(true);
    },
  );

  test('unmount stops the calls', async () => {
    vi.mocked(api.get).mockImplementation((() => failing(503)) as never);
    const { unmount } = mount();
    await advance(10_000);
    unmount();
    const atUnmount = calls();
    await advance(5 * 60_000);
    expect(calls()).toBe(atUnmount);
  });

  test('the cap stops polling and gives up', async () => {
    vi.mocked(api.get).mockImplementation((() => failing(503)) as never);
    const { result } = mount();
    await advance(CREDENTIALS_POLL_CAP_MS + 1_000);
    expect(result.current.gaveUp).toBe(true);
    const atCap = calls();
    await advance(5 * 60_000);
    expect(calls()).toBe(atCap);
  });
});
