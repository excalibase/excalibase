import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { LoginPage } from './LoginPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

const PASSWORD = ['Login', '7', 'pass'].join('');

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

function renderLogin() {
  return render(
    <MemoryRouter initialEntries={['/login']}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/" element={<div data-testid="home">HOME</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('sign-in identifier', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [] } } as never);
  });

  test('one field takes a username or an e-mail address', () => {
    renderLogin();
    const field = screen.getByLabelText('Username or e-mail');
    expect(field).toHaveAttribute('autocomplete', 'username');
    expect(field).toHaveAttribute('placeholder', 'you@company.com');
  });

  test('an e-mail address is sent as typed, trimmed', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { user: { id: 'u1', username: 'erin-3fa9c1', email: 'Erin@x.test', role: 'user' } } } as never);
    renderLogin();

    await u.type(screen.getByLabelText('Username or e-mail'), '  Erin@x.test ');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/auth/login', { username: 'Erin@x.test', password: PASSWORD }));
    expect(await screen.findByTestId('home')).toBeInTheDocument();
  });

  test('an unverified sign-in by e-mail prefills the resend form with that address', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { status: 403, data: { code: 'email_not_verified' } } });
    renderLogin();

    await u.type(screen.getByLabelText('Username or e-mail'), ' erin@x.test ');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    expect(await screen.findByLabelText('Your email')).toHaveValue('erin@x.test');
  });

  test('an unverified sign-in by username leaves the resend address for the user', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { status: 403, data: { code: 'email_not_verified' } } });
    renderLogin();

    await u.type(screen.getByLabelText('Username or e-mail'), 'erin');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    expect(await screen.findByLabelText('Your email')).toHaveValue('');
  });

  test('a refused sign-in names both identifiers', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { status: 401, data: {} } });
    renderLogin();

    await u.type(screen.getByLabelText('Username or e-mail'), 'erin@x.test');
    await u.type(screen.getByLabelText('Password'), PASSWORD);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    expect(await screen.findByText('Invalid username, e-mail or password')).toBeInTheDocument();
  });

  test('an empty form asks for both fields without a request', async () => {
    const u = userEvent.setup();
    renderLogin();
    await u.click(screen.getByRole('button', { name: /sign in/i }));
    expect(screen.getByText('Username or e-mail and password are required')).toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();
  });
});
