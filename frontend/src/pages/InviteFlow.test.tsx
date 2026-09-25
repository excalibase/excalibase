import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { RegisterPage } from './RegisterPage';
import { LoginPage } from './LoginPage';
import { InviteLinkNotice } from '../components/InviteLinkNotice';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

const TEST_PASSWORD_PLACEHOLDER = ['Invitee', '1', '!'].join('');

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

const user = { id: 'u1', username: 'carol', email: 'carol@x.test', role: 'user' };

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/register" element={<RegisterPage />} />
        <Route path="/login" element={<LoginPage />} />
        <Route path="/orgs" element={<div data-testid="orgs">ORGS</div>} />
        <Route path="/" element={<div data-testid="home">HOME</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('invite link flow', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
    vi.mocked(api.post).mockResolvedValue({ data: { token: 't', user } } as never);
  });

  test('registration sends the invite token carried by the link', async () => {
    const u = userEvent.setup();
    renderAt('/register?invite=tok123');
    expect(screen.getByTestId('invite-banner')).toBeInTheDocument();

    await u.type(screen.getByLabelText('Username'), 'carol');
    await u.type(screen.getByLabelText('Email'), 'carol@x.test');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /create account/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/auth/register', {
      username: 'carol',
      email: 'carol@x.test',
      password: TEST_PASSWORD_PLACEHOLDER,
      inviteToken: 'tok123',
    }));
    expect(await screen.findByTestId('orgs')).toBeInTheDocument();
  });

  test('registration without a link sends no invite token', async () => {
    const u = userEvent.setup();
    renderAt('/register');
    expect(screen.queryByTestId('invite-banner')).not.toBeInTheDocument();

    await u.type(screen.getByLabelText('Username'), 'dave');
    await u.type(screen.getByLabelText('Email'), 'dave@x.test');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /create account/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    const body = vi.mocked(api.post).mock.calls[0][1] as Record<string, unknown>;
    expect(body).not.toHaveProperty('inviteToken');
  });

  test('the sign-in link keeps the invite', () => {
    renderAt('/register?invite=tok123');
    expect(screen.getByRole('link', { name: /sign in/i })).toHaveAttribute('href', '/login?invite=tok123');
  });

  test('signing in through a link accepts the invite', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockImplementation((url: string) => {
      if (url === '/auth/login') return Promise.resolve({ data: { token: 't', user } } as never);
      if (url === '/orgs/invites/accept') return Promise.resolve({ data: { orgId: 'o1', role: 'developer' } } as never);
      return Promise.reject(new Error(`unexpected POST ${url}`));
    });
    renderAt('/login?invite=tok123');

    await u.type(screen.getByLabelText('Username'), 'carol');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/orgs/invites/accept', { token: 'tok123' }));
    expect(await screen.findByTestId('orgs')).toBeInTheDocument();
  });

  test('a refused invite on sign-in is reported, not swallowed', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockImplementation((url: string) => {
      if (url === '/auth/login') return Promise.resolve({ data: { token: 't', user } } as never);
      return Promise.reject({ response: { status: 400, data: { error: 'invite link is invalid or has expired' } } });
    });
    renderAt('/login?invite=spent');

    await u.type(screen.getByLabelText('Username'), 'carol');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    expect(await screen.findByText('invite link is invalid or has expired')).toBeInTheDocument();
  });

  test('plain sign-in accepts nothing', async () => {
    const u = userEvent.setup();
    renderAt('/login');
    await u.type(screen.getByLabelText('Username'), 'carol');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /sign in/i }));

    expect(await screen.findByTestId('home')).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledTimes(1);
  });
});

describe('sign-in and registration errors', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
  });

  test('empty forms are refused without a request', async () => {
    const u = userEvent.setup();
    const { unmount } = renderAt('/login');
    await u.click(screen.getByRole('button', { name: /sign in/i }));
    expect(screen.getByText('Username and password are required')).toBeInTheDocument();
    unmount();

    renderAt('/register');
    await u.click(screen.getByRole('button', { name: /create account/i }));
    expect(screen.getByText('All fields are required')).toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();
  });

  test.each([
    [401, undefined, 'Invalid username or password'],
    [500, 'boom', 'boom'],
    [500, undefined, 'Login failed. Please try again.'],
  ])('sign-in failure %i reports %s', async (status, error, shown) => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { status, data: error ? { error } : {} } });
    renderAt('/login?invite=tok');
    await u.type(screen.getByLabelText('Username'), 'carol');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /sign in/i }));
    expect(await screen.findByText(shown)).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test.each([
    [409, 'username already taken', 'username already taken'],
    [409, undefined, 'Account already exists'],
    [400, 'invite link is invalid or has expired', 'invite link is invalid or has expired'],
    [500, undefined, 'Registration failed'],
  ])('registration failure %i reports %s', async (status, error, shown) => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { status, data: error ? { error } : {} } });
    renderAt('/register?invite=tok');
    await u.type(screen.getByLabelText('Username'), 'carol');
    await u.type(screen.getByLabelText('Email'), 'carol@x.test');
    await u.type(screen.getByLabelText('Password'), TEST_PASSWORD_PLACEHOLDER);
    await u.click(screen.getByRole('button', { name: /create account/i }));
    expect(await screen.findByText(shown)).toBeInTheDocument();
  });

  test('the register link on sign-in keeps the invite', () => {
    renderAt('/login?invite=tok');
    expect(screen.getByRole('link', { name: /register/i })).toHaveAttribute('href', '/register?invite=tok');
  });
});

describe('InviteLinkNotice', () => {
  test('a failed copy tells the admin to copy by hand', async () => {
    const u = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
      configurable: true,
    });
    render(<InviteLinkNotice email="carol@x.test" link="/register?invite=tok" />);
    await u.click(screen.getByTestId('invite-link'));
    await u.click(screen.getByRole('button', { name: /copy/i }));
    expect(await screen.findByText(/copy failed/i)).toBeInTheDocument();
  });

  test('shows the full link on this origin and copies it', async () => {
    const u = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });

    render(<InviteLinkNotice email="carol@x.test" link="/register?invite=tok123" />);
    const expected = `${window.location.origin}/register?invite=tok123`;
    expect(screen.getByTestId('invite-link')).toHaveValue(expected);

    await u.click(screen.getByRole('button', { name: /copy/i }));
    expect(writeText).toHaveBeenCalledWith(expected);
    expect(await screen.findByText(/copied/i)).toBeInTheDocument();
  });
});
