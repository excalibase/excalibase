import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { usePostgresCatalog, findMajor } from './postgresCatalog';
import { api } from './client';

vi.mock('./client', () => ({ api: { get: vi.fn() } }));

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return Wrapper;
}

describe('usePostgresCatalog', () => {
  beforeEach(() => vi.clearAllMocks());

  test('reads the supported majors from the control plane', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: {
        documentDbRef: 'v0.117-0',
        majors: [
          { major: '14', available: true, documentDb: false, documentDbUnavailableReason: 'no' },
          { major: '15', available: true, documentDb: true },
        ],
      },
    } as never);

    const { result } = renderHook(() => usePostgresCatalog(), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(api.get).toHaveBeenCalledWith('/postgres/catalog');
    expect(result.current.data?.majors.map((m) => m.major)).toEqual(['14', '15']);
  });

  // The whole point of the endpoint: a catalogue change reaches the form
  // without a Studio release. A hardcoded list would fail this.
  test('offers whatever the catalogue offers, in its order', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({
      data: { majors: [{ major: '19', available: true, documentDb: true }] },
    } as never);

    const { result } = renderHook(() => usePostgresCatalog(), { wrapper: makeWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.majors.map((m) => m.major)).toEqual(['19']);
  });
});

describe('findMajor', () => {
  const catalog = {
    majors: [
      { major: '14', available: true, documentDb: false, documentDbUnavailableReason: 'needs 15+' },
      { major: '16', available: true, documentDb: true },
    ],
  };

  test('returns the entry for a catalogued major', () => {
    expect(findMajor(catalog, '16')?.documentDb).toBe(true);
  });

  test('returns undefined rather than a stand-in for a major the catalogue does not list', () => {
    expect(findMajor(catalog, '18')).toBeUndefined();
    expect(findMajor(undefined, '16')).toBeUndefined();
    expect(findMajor(catalog, '')).toBeUndefined();
  });
});
