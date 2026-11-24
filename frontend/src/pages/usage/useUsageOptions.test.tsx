import { describe, test, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useUsageOptions } from './useOrgUsage';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn() } }));

function run(config: Promise<{ data: object }>) {
  vi.mocked(api.get).mockReturnValue(config as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return renderHook(() => useUsageOptions(), { wrapper });
}

describe('useUsageOptions', () => {
  beforeEach(() => vi.clearAllMocks());

  test('while the config loads nothing reads as self-hosted, though the mode defaults to it', () => {
    const { result } = run(new Promise(() => {}));
    expect(result.current.ready).toBe(false);
    expect(result.current.selfHosted).toBe(false);
  });

  test('a self-hosted install is self-hosted once the config answers', async () => {
    const { result } = run(Promise.resolve({ data: { deploymentMode: 'selfhosted' } }));
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.selfHosted).toBe(true);
  });

  test('a cloud install is not self-hosted once the config answers', async () => {
    const { result } = run(Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } }));
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.selfHosted).toBe(false);
    expect(result.current.appsOn).toBe(true);
  });
});
