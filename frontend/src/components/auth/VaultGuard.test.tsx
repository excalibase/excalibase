import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { VaultGuard } from './VaultGuard';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

function renderWithRoutes(initialEntries: string[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={initialEntries}>
        <Routes>
          <Route element={<VaultGuard />}>
            <Route path="/setup" element={<div data-testid="setup-page">SETUP</div>} />
            <Route path="/" element={<div data-testid="home">HOME</div>} />
            <Route path="/login" element={<div data-testid="login">LOGIN</div>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function mockGuardState(opts: { initialized?: boolean; sealed?: boolean; hasAdmin?: boolean } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/vault/status') {
      return Promise.resolve({
        data: {
          initialized: opts.initialized ?? true,
          sealed: opts.sealed ?? false,
          threshold: 1,
          shares: 1,
          progress: 0,
          type: 'shamir',
        },
      } as never);
    }
    if (url === '/auth/setup-status') {
      return Promise.resolve({ data: { hasAdmin: opts.hasAdmin ?? true } } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

describe('VaultGuard', () => {
  beforeEach(() => vi.clearAllMocks());

  test('redirects to /setup when vault is uninitialized', async () => {
    mockGuardState({ initialized: false, sealed: true, hasAdmin: false });
    renderWithRoutes(['/']);
    expect(await screen.findByTestId('setup-page')).toBeInTheDocument();
    expect(screen.queryByTestId('home')).not.toBeInTheDocument();
  });

  test('redirects to /setup when vault is initialized but sealed', async () => {
    mockGuardState({ initialized: true, sealed: true, hasAdmin: true });
    renderWithRoutes(['/']);
    expect(await screen.findByTestId('setup-page')).toBeInTheDocument();
  });

  test('redirects to /setup when no admin user exists', async () => {
    mockGuardState({ initialized: true, sealed: false, hasAdmin: false });
    renderWithRoutes(['/']);
    expect(await screen.findByTestId('setup-page')).toBeInTheDocument();
  });

  test('lets app render when initialized + unsealed + admin exists', async () => {
    mockGuardState({ initialized: true, sealed: false, hasAdmin: true });
    renderWithRoutes(['/']);
    expect(await screen.findByTestId('home')).toBeInTheDocument();
  });

  test('redirects /setup back to / when setup is complete', async () => {
    mockGuardState({ initialized: true, sealed: false, hasAdmin: true });
    renderWithRoutes(['/setup']);
    expect(await screen.findByTestId('home')).toBeInTheDocument();
    expect(screen.queryByTestId('setup-page')).not.toBeInTheDocument();
  });

  test('keeps user on /setup while wizard is incomplete', async () => {
    mockGuardState({ initialized: false, sealed: true, hasAdmin: false });
    renderWithRoutes(['/setup']);
    expect(await screen.findByTestId('setup-page')).toBeInTheDocument();
  });
});
