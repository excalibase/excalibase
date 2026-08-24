import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  useVaultStatus,
  useInitVault,
  useUnsealVault,
  useVaultSecretsList,
  useVaultSecret,
  useDeleteVaultSecret,
} from './useVault';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    delete: vi.fn(),
  },
}));

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, Wrapper };
}

describe('useVaultStatus', () => {
  beforeEach(() => vi.clearAllMocks());

  test('GETs /vault/status and exposes the HashiCorp-shape response', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: { initialized: true, sealed: false, threshold: 3, shares: 5, progress: 0, type: 'shamir' },
    } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useVaultStatus(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/vault/status');
    expect(result.current.data?.threshold).toBe(3);
    expect(result.current.data?.shares).toBe(5);
  });
});

describe('useInitVault', () => {
  beforeEach(() => vi.clearAllMocks());

  test('POSTs /vault/init with shares + threshold body', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { shares: ['hex1', 'hex2', 'hex3'], threshold: 2 },
    } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useInitVault(), { wrapper: Wrapper });
    result.current.mutate({ shares: 3, threshold: 2 });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/vault/init', { shares: 3, threshold: 2 });
    expect(result.current.data?.shares).toHaveLength(3);
  });

  test('invalidates vault status query on success', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { shares: ['x'], threshold: 1 } } as never);
    const { client, Wrapper } = makeWrapper();
    client.setQueryData(['vault', 'status'], {});
    const { result } = renderHook(() => useInitVault(), { wrapper: Wrapper });
    result.current.mutate({ shares: 1, threshold: 1 });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(client.getQueryState(['vault', 'status'])?.isInvalidated).toBe(true);
  });
});

describe('useUnsealVault', () => {
  beforeEach(() => vi.clearAllMocks());

  test('POSTs /vault/unseal with share string', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { sealed: false, progress: 0, threshold: 1 },
    } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useUnsealVault(), { wrapper: Wrapper });
    result.current.mutate('abc-share-hex');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/vault/unseal', { share: 'abc-share-hex' });
    expect(result.current.data?.sealed).toBe(false);
  });

  test('exposes sealed=true when more shares are needed', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { sealed: true, progress: 1, threshold: 3 },
    } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useUnsealVault(), { wrapper: Wrapper });
    result.current.mutate('share-1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ sealed: true, progress: 1, threshold: 3 });
  });
});

describe('useVaultSecretsList', () => {
  beforeEach(() => vi.clearAllMocks());

  test('GETs /vault/secrets-list and unwraps the paths array', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: { paths: ['projects/foo/proj/credentials/excalibase_app', 'pki/signing/private'] },
    } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useVaultSecretsList(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/vault/secrets-list');
    expect(result.current.data).toHaveLength(2);
  });

  test('passes prefix as query param when provided', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { paths: [] } } as never);
    const { Wrapper } = makeWrapper();
    renderHook(() => useVaultSecretsList('projects/foo'), { wrapper: Wrapper });
    await waitFor(() => expect(api.get).toHaveBeenCalled());
    expect(api.get).toHaveBeenCalledWith('/vault/secrets-list?prefix=projects%2Ffoo');
  });
});

describe('useVaultSecret', () => {
  beforeEach(() => vi.clearAllMocks());

  test('skips fetch when path is null', async () => {
    const { Wrapper } = makeWrapper();
    renderHook(() => useVaultSecret(null), { wrapper: Wrapper });
    await new Promise((r) => setTimeout(r, 30));
    expect(api.get).not.toHaveBeenCalled();
  });

  test('GETs /vault/secrets/{path} when given a path', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: { username: 'excalibase_app', password: 'secret' },
    } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(
      () => useVaultSecret('projects/foo/proj/credentials/excalibase_app'),
      { wrapper: Wrapper },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/vault/secrets/projects/foo/proj/credentials/excalibase_app');
    expect(result.current.data?.username).toBe('excalibase_app');
  });
});

describe('useDeleteVaultSecret', () => {
  beforeEach(() => vi.clearAllMocks());

  test('DELETEs /vault/secrets/{path}', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeleteVaultSecret(), { wrapper: Wrapper });
    result.current.mutate('projects/foo/proj/credentials/excalibase_app');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/vault/secrets/projects/foo/proj/credentials/excalibase_app');
  });

  test('invalidates the secrets list on success', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { client, Wrapper } = makeWrapper();
    client.setQueryData(['vault', 'secrets', ''], []);
    const { result } = renderHook(() => useDeleteVaultSecret(), { wrapper: Wrapper });
    result.current.mutate('any-path');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(client.getQueryState(['vault', 'secrets', ''])?.isInvalidated).toBe(true);
  });
});
