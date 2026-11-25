import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useProjectEndpoint } from './projectEndpoint';
import { api } from './client';

vi.mock('./client', () => ({ api: { get: vi.fn() } }));

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return Wrapper;
}

const RESPONSE = {
  projectId: 'proj-1',
  publicEnabled: true,
  available: true,
  host: 'proj-1.db.excalibase.io',
  port: 26257,
  requireTls: true,
  database: 'appdb',
  username: 'app',
  connectionStrings: {
    requireTls: 'postgresql://app@proj-1.db.excalibase.io:26257/appdb?sslmode=verify-full',
    allowPlaintext: 'postgresql://app@proj-1.db.excalibase.io:26257/appdb?sslmode=prefer',
  },
  caCertificate: '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n',
};

describe('useProjectEndpoint', () => {
  beforeEach(() => vi.clearAllMocks());

  test('reads one project database endpoint from the control plane', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: RESPONSE } as never);

    const { result } = renderHook(() => useProjectEndpoint('proj-1'), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/db-endpoint');
    expect(result.current.data?.host).toBe('proj-1.db.excalibase.io');
    expect(result.current.data?.internal).toBeUndefined();
  });

  test('reads the Mongo half the control plane serves for a DocumentDB project', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: {
        ...RESPONSE,
        mongoPort: 30222,
        mongoAvailable: false,
      },
    } as never);

    const { result } = renderHook(() => useProjectEndpoint('proj-1'), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.mongo).toEqual({ available: false, port: 30222 });
  });

  test('an admin response that carries the cluster address is not turned into a Mongo address', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: { ...RESPONSE, publicEnabled: false, available: false, port: 0, internal: { host: 'h', port: 5432, mongoHost: 'm', mongoPort: 10260 } },
    } as never);

    const { result } = renderHook(() => useProjectEndpoint('proj-1'), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.mongo).toBeUndefined();
  });

  // Absent must stay absent rather than becoming a default a page would
  // print as fact.
  test('leaves the Mongo half undefined when the response carries none', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: RESPONSE } as never);

    const { result } = renderHook(() => useProjectEndpoint('proj-1'), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.mongo).toBeUndefined();
  });

  test('asks for nothing without a project', () => {
    renderHook(() => useProjectEndpoint(undefined), { wrapper: makeWrapper() });
    expect(api.get).not.toHaveBeenCalled();
  });
});
