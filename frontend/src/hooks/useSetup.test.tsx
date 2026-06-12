import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useSetupStatus, useRegisterAdmin } from './useSetup';
import { api } from '../api/client';

// Non-secret placeholder password used only in test assertions/fixtures.
const TEST_PASSWORD_PLACEHOLDER = ['Founder', '1', '!'].join('');

vi.mock('../api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
  },
}));

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, Wrapper };
}

describe('useSetupStatus', () => {
  beforeEach(() => vi.clearAllMocks());

  test('GETs /auth/setup-status and returns hasAdmin', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { hasAdmin: false } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSetupStatus(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/auth/setup-status');
    expect(result.current.data?.hasAdmin).toBe(false);
  });

  test('reflects hasAdmin=true once the platform has an admin', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { hasAdmin: true } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSetupStatus(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.hasAdmin).toBe(true);
  });
});

describe('useRegisterAdmin', () => {
  beforeEach(() => vi.clearAllMocks());

  test('POSTs /auth/register and returns the freshly-minted PAT', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: {
        token: 'pat-bootstrap',
        user: { id: 'u1', username: 'founder', email: 'a@b.c', role: 'platform_admin' },
      },
    } as never);

    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useRegisterAdmin(), { wrapper: Wrapper });
    result.current.mutate({ username: 'founder', email: 'a@b.c', password: TEST_PASSWORD_PLACEHOLDER });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/auth/register', {
      username: 'founder',
      email: 'a@b.c',
      password: TEST_PASSWORD_PLACEHOLDER,
    });
    expect(result.current.data?.token).toBe('pat-bootstrap');
    expect(result.current.data?.user.role).toBe('platform_admin');
  });

  test('invalidates setup-status so VaultGuard re-evaluates', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: {
        token: 't',
        user: { id: 'u1', username: 'a', email: 'a@b.c', role: 'platform_admin' },
      },
    } as never);

    const { client, Wrapper } = makeWrapper();
    client.setQueryData(['setup', 'status'], { hasAdmin: false });

    const { result } = renderHook(() => useRegisterAdmin(), { wrapper: Wrapper });
    result.current.mutate({ username: 'a', email: 'a@b.c', password: 'P1!' });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(client.getQueryState(['setup', 'status'])?.isInvalidated).toBe(true);
  });
});
