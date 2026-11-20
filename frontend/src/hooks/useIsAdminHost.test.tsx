import { describe, test, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useIsAdminHost } from './useDeploymentMode';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

function run(config: object, host: string) {
  vi.mocked(api.get).mockResolvedValue({ data: config } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return renderHook(() => useIsAdminHost(host), { wrapper });
}

describe('useIsAdminHost', () => {
  beforeEach(() => vi.clearAllMocks());

  test('is true on the configured admin host, whatever its case', async () => {
    const { result } = run({ deploymentMode: 'cloud', adminHost: 'admin.excalibase.io' }, 'Admin.Excalibase.io');
    await waitFor(() => expect(result.current).toBe(true));
  });

  test('is false on the app host', async () => {
    const { result } = run({ deploymentMode: 'cloud', adminHost: 'admin.excalibase.io' }, 'app.excalibase.io');
    await waitFor(() => expect(api.get).toHaveBeenCalled());
    expect(result.current).toBe(false);
  });

  test('is false everywhere when no admin host is configured', async () => {
    const { result } = run({ deploymentMode: 'cloud' }, 'admin.excalibase.io');
    await waitFor(() => expect(api.get).toHaveBeenCalled());
    expect(result.current).toBe(false);
  });
});
