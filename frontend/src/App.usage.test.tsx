import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import App from './App';
import { api } from './api/client';
import { useAuthStore } from './stores/auth-store';

vi.mock('./api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function serve(features: object) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const bodies: Record<string, unknown> = {
      '/config': { deploymentMode: 'cloud', features },
      '/vault/status': { initialized: true, sealed: false },
      '/auth/setup-status': { hasAdmin: true },
    };
    return Promise.resolve({ data: bodies[url] ?? [] }) as never;
  });
}

function renderAt(path: string) {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user' }, isAuthenticated: true });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}><App /></MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('/usage belongs to studio_v2', () => {
  beforeEach(() => vi.clearAllMocks());

  test('flag off: the page is not served', async () => {
    serve({ studio_v2: false });
    renderAt('/usage');
    expect(await screen.findByTestId('feature-unavailable')).toBeInTheDocument();
    expect(screen.queryByTestId('usage-page')).not.toBeInTheDocument();
  });

  test('flag on: the page is served', async () => {
    serve({ studio_v2: true });
    renderAt('/usage');
    expect(await screen.findByTestId('usage-page')).toBeInTheDocument();
  });
});

describe.each([
  ['/account', 'account-page'],
  ['/project/p1/variables', 'project-variables'],
])('%s belongs to studio_v2', (path, testId) => {
  beforeEach(() => vi.clearAllMocks());

  test('flag off: the page is not served', async () => {
    serve({ studio_v2: false });
    renderAt(path);
    expect(await screen.findByTestId('feature-unavailable')).toBeInTheDocument();
    expect(screen.queryByTestId(testId)).not.toBeInTheDocument();
  });

  test('flag on: the page is served', async () => {
    serve({ studio_v2: true });
    renderAt(path);
    expect(await screen.findByTestId(testId)).toBeInTheDocument();
  });
});

describe('/admin/users/:userId belongs to studio_v2', () => {
  beforeEach(() => vi.clearAllMocks());

  test('flag off: the account page is not served, even to a platform admin', async () => {
    serve({ studio_v2: false });
    renderAt('/admin/users/u1');
    useAuthStore.setState({ user: { id: 'boss', username: 'boss', email: 'boss@example.test', role: 'platform_admin' }, isAuthenticated: true });
    expect(await screen.findByTestId('feature-unavailable')).toBeInTheDocument();
    expect(screen.queryByTestId('admin-user-detail')).not.toBeInTheDocument();
  });
});
