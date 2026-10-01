import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useProjectRole } from './useProjectRole';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return Wrapper;
}

function mockOrgRole(role: string | undefined, orgId = 'o1') {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/provision/p1') return Promise.resolve({ data: { projectId: 'p1', orgId } } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role }] } as never);
    return Promise.reject(new Error('unexpected ' + url));
  });
}

describe('useProjectRole', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().setAuth({ id: 'u1', username: 'dev', email: 'd@x.test', role: 'user' });
  });

  test.each([
    ['owner', true, true],
    ['admin', true, true],
    ['developer', true, false],
    ['viewer', false, false],
  ])('org role %s: develop=%s admin=%s', async (role, canDevelop, canAdmin) => {
    mockOrgRole(role);
    const { result } = renderHook(() => useProjectRole('p1'), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.role).toBe(role));
    expect(result.current.canDevelop).toBe(canDevelop);
    expect(result.current.canAdmin).toBe(canAdmin);
  });

  test('a caller not in the project org gets nothing', async () => {
    mockOrgRole('owner', 'other-org');
    const { result } = renderHook(() => useProjectRole('p1'), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.role).toBeNull();
    expect(result.current.canDevelop).toBe(false);
  });

  test('an unknown role grants nothing', async () => {
    mockOrgRole('guest');
    const { result } = renderHook(() => useProjectRole('p1'), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.role).toBe('guest'));
    expect(result.current.canDevelop).toBe(false);
  });

  test('a platform admin may do everything', async () => {
    useAuthStore.getState().setAuth({ id: 'u1', username: 'root', email: 'r@x.test', role: 'platform_admin' });
    mockOrgRole(undefined);
    const { result } = renderHook(() => useProjectRole('p1'), { wrapper: wrapper() });
    expect(result.current.canAdmin).toBe(true);
    expect(result.current.canDevelop).toBe(true);
  });

  test('a failed lookup grants nothing', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('down'));
    const { result } = renderHook(() => useProjectRole('p1'), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.canDevelop).toBe(false);
  });
});
