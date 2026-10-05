import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { RegisterPage } from './RegisterPage';
import { LoginPage } from './LoginPage';
import { VerifyEmailPage } from './VerifyEmailPage';
import { ResetPasswordPage } from './ResetPasswordPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

const PASSWORD = ['Studio', '9', 'pass'].join('');

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/register" element={<RegisterPage />} />
        <Route path="/login" element={<LoginPage />} />
        <Route path="/verify-email" element={<VerifyEmailPage />} />
        <Route path="/reset-password" element={<ResetPasswordPage />} />
        <Route path="/" element={<div data-testid="home">HOME</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('Studio email verification', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [] } } as never);
  });

  test('sign-up asks the developer to confirm their address and signs nobody in', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'verification_required', email: 'dev@x.test' } } as never);
    renderAt('/register');

    await u.type(screen.getByLabelText('Username'), 'dev');
    await u.type(screen.getByLabelText('Email'), 'dev@x.test');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /create account/i }));

    expect(await screen.findByTestId('check-email')).toHaveTextContent('dev@x.test');
    expect(useAuthStore.getState().user).toBeNull();

    await u.click(screen.getByRole('button', { name: /send a new link/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/email/verify/resend', { email: 'dev@x.test' }));
    expect(await screen.findByText(/new link is on its way/i)).toBeInTheDocument();
  });

  test('sign-in keeps the session token out of script-readable storage', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: { token: 'session-secret', user: { id: 'u1', username: 'dev', email: 'dev@x.test', role: 'user' } },
    } as never);
    renderAt('/login');

    await u.type(screen.getByLabelText('Username or e-mail'), 'dev');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    await waitFor(() => expect(useAuthStore.getState().isAuthenticated).toBe(true));
    expect(JSON.stringify(localStorage)).not.toContain('session-secret');
    expect(JSON.stringify(sessionStorage)).not.toContain('session-secret');
  });

  test('sign-in of an unverified account offers a new link', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockImplementation((url: string) => {
      if (url === '/auth/login') {
        return Promise.reject({ response: { status: 403, data: { code: 'email_not_verified', error: 'email not verified' } } });
      }
      return Promise.resolve({ data: { status: 'sent' } } as never);
    });
    renderAt('/login');

    await u.type(screen.getByLabelText('Username or e-mail'), 'dev');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    expect(await screen.findByTestId('email-not-verified')).toBeInTheDocument();
    await u.type(screen.getByLabelText('Your email'), 'dev@x.test');
    await u.click(screen.getByRole('button', { name: /send a new link/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/email/verify/resend', { email: 'dev@x.test' }));
    expect(useAuthStore.getState().user).toBeNull();
  });

  test('the emailed link confirms the address', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'verified' } } as never);
    renderAt('/verify-email?token=tok123');

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/email/verify/confirm', { token: 'tok123' }));
    expect(await screen.findByTestId('email-verified')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /sign in/i })).toHaveAttribute('href', '/login');
  });

  test('an expired link says so and offers a new one', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 400, data: { error: 'verification link is invalid or has expired' } } });
    renderAt('/verify-email?token=old');

    expect(await screen.findByText('verification link is invalid or has expired')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /send a new link/i })).toBeInTheDocument();
  });

  test('a link without a token is refused without a request', () => {
    renderAt('/verify-email');
    expect(screen.getByText(/link is incomplete/i)).toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('the password-reset link sets a new password', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'reset' } } as never);
    renderAt('/reset-password?token=rst');

    await u.type(screen.getByLabelText('New password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /set password/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/email/reset/confirm', { token: 'rst', newPassword: PASSWORD }));
    expect(await screen.findByTestId('password-reset')).toBeInTheDocument();
  });

  test('after a reset the user is told their access tokens were revoked', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'reset', accessTokensRevoked: 2 } } as never);
    renderAt('/reset-password?token=rst');

    await u.type(screen.getByLabelText('New password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /set password/i }));

    const notice = await screen.findByTestId('tokens-revoked');
    expect(notice).toHaveTextContent(/2 personal access tokens were revoked/i);
    expect(notice).toHaveTextContent(/signed out/i);
  });

  test('after a reset the user is told which username signs in', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'reset', accessTokensRevoked: 0, username: 'erin-3fa9c1' } } as never);
    renderAt('/reset-password?token=rst');

    await u.type(screen.getByLabelText('New password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /set password/i }));

    expect(await screen.findByTestId('sign-in-as')).toHaveTextContent('Sign in as erin-3fa9c1 or with your e-mail');
  });

  test('a reset with no access tokens still says every session was signed out', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'reset', accessTokensRevoked: 0 } } as never);
    renderAt('/reset-password?token=rst');

    await u.type(screen.getByLabelText('New password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /set password/i }));

    const notice = await screen.findByTestId('tokens-revoked');
    expect(notice).toHaveTextContent(/signed out/i);
    expect(notice).not.toHaveTextContent(/were revoked/i);
  });
});
