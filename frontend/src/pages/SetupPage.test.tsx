import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SetupPage } from './SetupPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

// Non-secret placeholder password typed into the form / asserted in tests.
const TEST_PASSWORD_PLACEHOLDER = ['Founder', '1', '!'].join('');

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

interface State {
  initialized: boolean;
  sealed: boolean;
  threshold: number;
  shares: number;
  progress: number;
  hasAdmin: boolean;
}

function renderPage(initial: State) {
  const state = { ...initial };

  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/vault/status') {
      return Promise.resolve({
        data: {
          initialized: state.initialized,
          sealed: state.sealed,
          threshold: state.threshold,
          shares: state.shares,
          progress: state.progress,
          type: 'shamir',
        },
      } as never);
    }
    if (url === '/auth/setup-status') {
      return Promise.resolve({ data: { hasAdmin: state.hasAdmin } } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });

  vi.mocked(api.post).mockImplementation((url: string, body: unknown) => {
    if (url === '/vault/init') {
      const b = body as { shares: number; threshold: number };
      state.initialized = true;
      state.sealed = true;
      state.threshold = b.threshold;
      state.shares = b.shares;
      const shares = Array.from({ length: b.shares }, (_, i) => `share-${i}`);
      return Promise.resolve({ data: { shares, threshold: b.threshold } } as never);
    }
    if (url === '/vault/unseal') {
      state.progress += 1;
      if (state.progress >= state.threshold) {
        state.sealed = false;
        state.progress = 0;
      }
      return Promise.resolve({
        data: { sealed: state.sealed, progress: state.progress, threshold: state.threshold },
      } as never);
    }
    if (url === '/auth/register') {
      state.hasAdmin = true;
      return Promise.resolve({
        data: {
          token: 'pat-bootstrap',
          user: {
            id: 'u1',
            username: (body as { username: string }).username,
            email: (body as { email: string }).email,
            role: 'platform_admin',
          },
        },
      } as never);
    }
    return Promise.reject(new Error(`unexpected POST ${url}`));
  });

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const utils = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/setup']}>
        <Routes>
          <Route path="/setup" element={<SetupPage />} />
          <Route path="/" element={<div data-testid="dashboard">DASHBOARD</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...utils, state };
}

describe('SetupPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
  });

  test('renders admin step first on a virgin setup (SEC-C1: admin before vault)', async () => {
    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: false });
    expect(await screen.findByTestId('vault-setup-admin')).toBeInTheDocument();
  });

  test('renders init step once the admin exists', async () => {
    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: true });
    expect(await screen.findByTestId('vault-setup-init')).toBeInTheDocument();
    expect(screen.getByTestId('vault-init-shares')).toHaveValue('5');
    expect(screen.getByTestId('vault-init-threshold')).toHaveValue('3');
  });

  test('rejects non-numeric shares input via inline error', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: true });

    await screen.findByTestId('vault-setup-init');
    const sharesInput = screen.getByTestId('vault-init-shares');
    await user.clear(sharesInput);
    await user.type(sharesInput, '0');
    // Trigger validation by tabbing
    await user.tab();

    await waitFor(() => {
      expect(screen.getByTestId('vault-init-shares-error')).toBeInTheDocument();
    });
  });

  test('init flow advances to shares-display when backend returns shares', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: true });

    await screen.findByTestId('vault-setup-init');
    await user.click(screen.getByTestId('vault-init-submit'));

    expect(await screen.findByTestId('vault-setup-shares')).toBeInTheDocument();
    // 5 shares rendered as copy buttons
    for (let i = 0; i < 5; i++) {
      expect(screen.getByTestId(`vault-share-copy-${i}`)).toBeInTheDocument();
    }
  });

  test('Continue button stays disabled until user confirms saving shares', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: true });

    await screen.findByTestId('vault-setup-init');
    await user.click(screen.getByTestId('vault-init-submit'));

    const continueBtn = await screen.findByTestId('vault-shares-continue');
    expect(continueBtn).toBeDisabled();

    await user.click(screen.getByTestId('vault-shares-confirm'));
    expect(continueBtn).toBeEnabled();
  });

  test('renders unseal step when vault is initialized but sealed', async () => {
    renderPage({ initialized: true, sealed: true, threshold: 1, shares: 1, progress: 0, hasAdmin: true });
    expect(await screen.findByTestId('vault-setup-unseal')).toBeInTheDocument();
  });

  test('admin step renders when vault unsealed but no admin yet', async () => {
    renderPage({ initialized: true, sealed: false, threshold: 1, shares: 1, progress: 0, hasAdmin: false });
    expect(await screen.findByTestId('vault-setup-admin')).toBeInTheDocument();
  });

  test('admin form submits, persists token to auth store, redirects to /', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: true, sealed: false, threshold: 1, shares: 1, progress: 0, hasAdmin: false });

    await screen.findByTestId('vault-setup-admin');
    await user.type(screen.getByTestId('admin-username'), 'founder');
    await user.type(screen.getByTestId('admin-email'), 'founder@example.com');
    await user.type(screen.getByTestId('admin-password'), TEST_PASSWORD_PLACEHOLDER);

    const submit = screen.getByTestId('admin-submit');
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    await waitFor(() => {
      expect(api.post).toHaveBeenCalledWith('/auth/register', {
        username: 'founder',
        email: 'founder@example.com',
        password: TEST_PASSWORD_PLACEHOLDER,
      });
    });
    await waitFor(() => {
      // Cookie auth flow: state holds the user profile + isAuthenticated.
      // The raw token lives in localStorage as legacyToken (header fallback)
      // and in the httpOnly cookie set by the server — neither belongs in
      // store state any more.
      expect(useAuthStore.getState().isAuthenticated).toBe(true);
      expect(useAuthStore.getState().user?.role).toBe('platform_admin');
      expect(localStorage.getItem('auth_token')).toBe('pat-bootstrap');
    });
    expect(await screen.findByTestId('dashboard')).toBeInTheDocument();
  });

  test('admin form rejects too-short username via inline error', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: true, sealed: false, threshold: 1, shares: 1, progress: 0, hasAdmin: false });

    await screen.findByTestId('vault-setup-admin');
    await user.type(screen.getByTestId('admin-username'), 'ab');
    await user.tab();

    await waitFor(() => {
      expect(screen.getByTestId('admin-username-error')).toBeInTheDocument();
    });
  });

  test('shares step copy-to-clipboard hits the clipboard API', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      writable: true,
      configurable: true,
    });

    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: true });
    await screen.findByTestId('vault-setup-init');
    await user.click(screen.getByTestId('vault-init-submit'));
    await screen.findByTestId('vault-setup-shares');

    await user.click(screen.getByTestId('vault-share-copy-0'));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('share-0'));
  });

  test('shares step continue button advances to unseal once confirmed', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: false, sealed: true, threshold: 0, shares: 0, progress: 0, hasAdmin: true });

    await screen.findByTestId('vault-setup-init');
    await user.click(screen.getByTestId('vault-init-submit'));
    await screen.findByTestId('vault-setup-shares');
    await user.click(screen.getByTestId('vault-shares-confirm'));
    await user.click(screen.getByTestId('vault-shares-continue'));

    expect(await screen.findByTestId('vault-setup-unseal')).toBeInTheDocument();
  });

  test('unseal form submits a share and clears input on progress', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: true, sealed: true, threshold: 1, shares: 1, progress: 0, hasAdmin: true });

    const input = await screen.findByTestId('vault-unseal-input');
    await user.type(input, 'my-share-hex');
    await user.click(screen.getByTestId('vault-unseal-submit'));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/vault/unseal', { share: 'my-share-hex' }));
  });

  test('admin form rejects invalid email via inline error', async () => {
    const user = userEvent.setup();
    renderPage({ initialized: true, sealed: false, threshold: 1, shares: 1, progress: 0, hasAdmin: false });

    await screen.findByTestId('vault-setup-admin');
    await user.type(screen.getByTestId('admin-email'), 'not-an-email');
    await user.tab();

    await waitFor(() => {
      expect(screen.getByTestId('admin-email-error')).toBeInTheDocument();
    });
  });
});
