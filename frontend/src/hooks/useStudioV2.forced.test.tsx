import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useStudioV2 } from './useDeploymentMode';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

function run(config: object) {
  vi.mocked(api.get).mockResolvedValue({ data: config } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return renderHook(() => useStudioV2(), { wrapper });
}

describe('useStudioV2 with VITE_FORCE_V2', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => vi.unstubAllEnvs());

  test('follows the server when the browser-test switch is not set', async () => {
    const { result } = run({ deploymentMode: 'cloud', features: { studio_v2: false } });
    await waitFor(() => expect(api.get).toHaveBeenCalled());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.enabled).toBe(false);
  });

  test('is on whatever the server says when the switch is 1', async () => {
    vi.stubEnv('VITE_FORCE_V2', '1');
    const { result } = run({ deploymentMode: 'cloud', features: { studio_v2: false } });
    await waitFor(() => expect(result.current.enabled).toBe(true));
  });

  test('is on when the server sends no features at all', async () => {
    vi.stubEnv('VITE_FORCE_V2', '1');
    const { result } = run({ deploymentMode: 'cloud' });
    await waitFor(() => expect(result.current.enabled).toBe(true));
  });

  test('ignores any other value of the switch', async () => {
    vi.stubEnv('VITE_FORCE_V2', 'true');
    const { result } = run({ deploymentMode: 'cloud', features: { studio_v2: false } });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.enabled).toBe(false);
  });

  test('stays off in a production build even when the switch is 1', async () => {
    vi.stubEnv('DEV', false);
    vi.stubEnv('VITE_FORCE_V2', '1');
    const { result } = run({ deploymentMode: 'cloud', features: { studio_v2: false } });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.enabled).toBe(false);
  });
});
