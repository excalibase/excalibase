import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { AccountPage } from './AccountPage';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const answers: Promise<unknown>[] = [];

// Waits for every answer the mocked API has given and for the screen to take them in, so an
// assertion that something is absent cannot pass just because nothing has loaded yet.
async function settle() {
  await act(async () => {
    await Promise.allSettled(answers);
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
}

function renderPage(me: Record<string, unknown> | null = { emailVerifiedAt: '2026-11-01T00:00:00Z' }) {
  answers.length = 0;
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user' }, isAuthenticated: true });
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url !== '/auth/me' || me === null) return Promise.reject(new Error(`unexpected GET ${url}`));
    const answer = Promise.resolve({ data: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user', ...me } });
    answers.push(answer);
    return answer as never;
  });
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter><AccountPage /></MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => { vi.clearAllMocks(); });

describe('AccountPage', () => {
  test('shows the profile from the signed-in account', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Account' })).toBeInTheDocument();
    expect(screen.getByText('dev@example.test')).toBeInTheDocument();
    expect(screen.getByText('dev')).toBeInTheDocument();
  });

  test('a verified address offers no verification mail', async () => {
    renderPage();
    expect(await screen.findByText('Verified')).toBeInTheDocument();
    await settle();
    expect(screen.queryByRole('button', { name: /send verification email/i })).not.toBeInTheDocument();
  });

  test('an unverified address can ask for a verification mail', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { status: 'sent' } });
    renderPage({ emailVerifiedAt: undefined });
    await userEvent.click(await screen.findByRole('button', { name: /send verification email/i }));
    expect(api.post).toHaveBeenCalledWith('/email/verify/send');
    expect(await screen.findByText(/verification email is on its way/i)).toBeInTheDocument();
  });

  test('a failed verification mail says so', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 429 } });
    renderPage({ emailVerifiedAt: undefined });
    await userEvent.click(await screen.findByRole('button', { name: /send verification email/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/too many/i);
  });

  test('the password changes through the existing reset link, with no password typed here', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { expiresInMinutes: 30 } });
    renderPage();
    await screen.findByText('Verified');
    expect(document.querySelector('input[type="password"]')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: /email me a reset link/i }));
    expect(api.post).toHaveBeenCalledWith('/email/reset/send', { email: 'dev@example.test' });
    expect(await screen.findByText(/expires in 30 minutes/i)).toBeInTheDocument();
  });

  test('a rate-limited reset request says to wait', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 429 } });
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /email me a reset link/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/wait a while/i);
  });

  test('a failed profile read still shows the account from sign-in', async () => {
    renderPage(null);
    expect(await screen.findByText('dev@example.test')).toBeInTheDocument();
  });

  test('access keys are one link away', async () => {
    renderPage();
    expect(await screen.findByRole('link', { name: /manage access keys/i })).toHaveAttribute('href', '/account/tokens');
  });
});
