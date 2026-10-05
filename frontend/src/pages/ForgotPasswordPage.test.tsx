import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ForgotPasswordPage } from './ForgotPasswordPage';
import { LoginPage } from './LoginPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/login" element={<div data-testid="login-page"><LoginPage /></div>} />
        <Route path="/forgot-password" element={<ForgotPasswordPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function requestLink(address: string) {
  const u = userEvent.setup();
  renderAt('/forgot-password');
  await u.type(screen.getByLabelText('Email'), address);
  await u.click(screen.getByRole('button', { name: /send reset link/i }));
  return u;
}

describe('Forgot password', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [] } } as never);
  });

  test('names the page in the browser tab', () => {
    renderAt('/forgot-password');
    expect(document.title).toBe('Forgot password · Excalibase Studio');
  });

  test('asks for a link and shows the same answer whatever the address, with the real expiry', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'sent', expiresInMinutes: 45 } } as never);
    await requestLink(' dev@x.test ');

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/email/reset/send', { email: 'dev@x.test' }));
    expect(await screen.findByTestId('reset-link-sent')).toHaveTextContent(
      "If an account exists for that address, we've sent a link. It expires in 45 minutes.",
    );
    expect(screen.queryByRole('button', { name: /send reset link/i })).not.toBeInTheDocument();
  });

  test('says when too many links were asked for', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 429, data: { error: 'rate limit exceeded' } } });
    await requestLink('dev@x.test');

    expect(await screen.findByRole('alert')).toHaveTextContent(/too many reset requests/i);
    expect(screen.queryByTestId('reset-link-sent')).not.toBeInTheDocument();
  });

  test('shows a failure and lets the user try again', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 500, data: {} } });
    await requestLink('dev@x.test');

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not send the reset link. Try again later.');
    expect(screen.getByRole('button', { name: /send reset link/i })).toBeEnabled();
    expect(screen.queryByTestId('reset-link-sent')).not.toBeInTheDocument();
  });

  test('does not send without an address', async () => {
    const u = userEvent.setup();
    renderAt('/forgot-password');
    expect(screen.getByRole('button', { name: /send reset link/i })).toBeDisabled();
    await u.type(screen.getByLabelText('Email'), '   ');
    expect(screen.getByRole('button', { name: /send reset link/i })).toBeDisabled();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('links back to sign in', async () => {
    const u = userEvent.setup();
    renderAt('/forgot-password');
    await u.click(screen.getByRole('link', { name: /back to sign in/i }));
    expect(await screen.findByTestId('login-page')).toBeInTheDocument();
  });

  test('the sign-in page links to it under the password field', async () => {
    const u = userEvent.setup();
    renderAt('/login');
    const link = screen.getByRole('link', { name: 'Forgot password?' });
    expect(link).toHaveAttribute('href', '/forgot-password');
    expect(screen.getByLabelText('Password').compareDocumentPosition(link) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    await u.click(link);
    expect(await screen.findByRole('button', { name: /send reset link/i })).toBeInTheDocument();
  });
});
