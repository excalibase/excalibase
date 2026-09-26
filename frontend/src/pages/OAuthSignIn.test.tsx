import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { LoginPage } from './LoginPage';
import { RegisterPage } from './RegisterPage';
import { OAuthCompletePage } from './OAuthCompletePage';
import { api } from '../api/client';
import { API_BASE } from '../api/base';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

const user = { id: 'u1', username: 'dev', email: 'dev@x.test', role: 'user' };

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        <Route path="/oauth/complete" element={<OAuthCompletePage />} />
        <Route path="/orgs" element={<div data-testid="orgs">ORGS</div>} />
        <Route path="/" element={<div data-testid="home">HOME</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('Studio sign-in with Google and GitHub', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
  });

  test('offers a button for each configured provider, carrying the invite', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { providers: ['google', 'github'] } } as never);
    renderAt('/login?invite=tok123');

    const google = await screen.findByRole('link', { name: /continue with google/i });
    expect(google).toHaveAttribute('href', `${API_BASE}/auth/oauth/google/start?invite=tok123`);
    expect(screen.getByRole('link', { name: /continue with github/i })).toHaveAttribute(
      'href', `${API_BASE}/auth/oauth/github/start?invite=tok123`);
  });

  test('offers nothing when no provider is configured', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [] } } as never);
    renderAt('/register');
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/auth/oauth/providers'));
    expect(screen.queryByRole('link', { name: /continue with/i })).not.toBeInTheDocument();
  });

  test('a refused provider sign-in is explained on the login page', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { providers: ['github'] } } as never);
    renderAt('/login?oauth_error=email_not_verified');
    expect(await screen.findByText(/has not verified that email/i)).toBeInTheDocument();
  });

  test('completing a sign-in loads the account from the session cookie', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: user } as never);
    renderAt('/oauth/complete');
    expect(await screen.findByTestId('home')).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/auth/me');
    expect(useAuthStore.getState().user).toEqual(user);
  });

  test('a sign-in that joined an org lands on the org list', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: user } as never);
    renderAt('/oauth/complete?joined=1');
    expect(await screen.findByTestId('orgs')).toBeInTheDocument();
  });

  test('a sign-in whose session did not stick goes back to login', async () => {
    vi.mocked(api.get).mockImplementation((url: string) =>
      url === '/auth/me'
        ? Promise.reject({ response: { status: 401 } })
        : Promise.resolve({ data: { providers: [] } } as never));
    renderAt('/oauth/complete');
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument();
    expect(useAuthStore.getState().user).toBeNull();
  });
});
