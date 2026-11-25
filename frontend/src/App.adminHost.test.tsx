import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import App from './App';
import { api } from './api/client';
import { useAuthStore } from './stores/auth-store';

vi.mock('./api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function serve(adminHost: string) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    // The host page's own data is not what is under test: it just stays loading.
    if (url.startsWith('/admin/monitoring')) return new Promise(() => {}) as never;
    const bodies: Record<string, unknown> = {
      '/config': { deploymentMode: 'selfhosted', adminHost, features: { hostmetrics: true, studio_v2: true } },
      '/vault/status': { initialized: true, sealed: false },
      '/auth/setup-status': { hasAdmin: true },
    };
    return Promise.resolve({ data: bodies[url] ?? [] }) as never;
  });
}

function renderAt(path: string) {
  useAuthStore.setState({ user: { id: '1', username: 'admin', email: 'a@e.test', role: 'platform_admin' }, isAuthenticated: true });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[path]}><App /></MemoryRouter>
    </QueryClientProvider>,
  );
}

const monitoringCalls = () => vi.mocked(api.get).mock.calls.filter(([url]) => String(url).startsWith('/admin/monitoring'));

describe('/admin/host is served only on the admin host', () => {
  beforeEach(() => vi.clearAllMocks());

  test('on another host it redirects to / and asks the server nothing', async () => {
    serve('admin.example.test');
    renderAt('/admin/host');
    expect(await screen.findByRole('navigation', { name: 'Main' })).toBeInTheDocument();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
    expect(screen.queryByTestId('host-cards')).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /host/i })).not.toBeInTheDocument();
    expect(monitoringCalls()).toHaveLength(0);
  });

  test('on the admin host the page is served and asks for the host data', async () => {
    serve('localhost');
    renderAt('/admin/host');
    await vi.waitFor(() => expect(monitoringCalls().length).toBeGreaterThan(0));
  });
});
