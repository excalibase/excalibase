import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { VerifyAccountCard } from './VerifyAccountCard';
import { api } from '../../api/client';
import type { TrustStatus } from '../../api/trust';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const POLICY = { minAgeDays: 90, minPublicEvents: 5, activityWindowDays: 90, minRepos: 3, repoMinAgeDays: 30 };
const limited = (extra: Partial<TrustStatus> = {}): TrustStatus => ({ state: 'limited', emailVerified: true, githubConfigured: true, policy: POLICY, ...extra });
const assign = vi.fn();

function mount(status: TrustStatus | 'unreachable', options: { trial?: boolean; route?: string } = {}) {
  vi.mocked(api.get).mockImplementation(((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features: { limitedtrial: options.trial ?? true } } });
    if (url === '/trust/') return status === 'unreachable' ? Promise.reject(new Error('down')) : Promise.resolve({ data: status });
    return Promise.resolve({ data: [] });
  }) as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[options.route ?? '/']}><VerifyAccountCard /></MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal('location', { assign });
});

describe('Verify your account card', () => {
  test('a limited account sees the rule from the server and one button that goes to GitHub', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { url: 'https://github.example/authorize' } });
    mount(limited());
    const card = await screen.findByRole('region', { name: 'Verify your account' });
    expect(card).toHaveTextContent('at least 90 days old');
    expect(card).toHaveTextContent('5 public events in the last 90 days');
    expect(card).toHaveTextContent('Your databases are not affected');
    await userEvent.click(screen.getByRole('button', { name: 'Verify with GitHub' }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://github.example/authorize'));
    expect(api.post).toHaveBeenCalledWith('/trust/github/start');
  });

  test('says what is missing after a failed check', async () => {
    mount(limited({ lastAttempt: { checkedAt: '2026-12-01T00:00:00Z', passed: false, reason: 'github_too_new', missing: ['Your GitHub account is 12 days old; it must be at least 90.'], evidence: { accountAgeDays: 12, publicEvents: 9, publicRepos: 0, oldestRepoAgeDays: 0 } } }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Not verified yet');
    expect(alert).toHaveTextContent('12 days old; it must be at least 90');
  });

  test('renders nothing for a verified account, with the trial off, while loading, or when the status cannot be read', async () => {
    const { unmount } = mount({ ...limited(), state: 'full' });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/trust/'));
    expect(screen.queryByRole('region', { name: 'Verify your account' })).not.toBeInTheDocument();
    unmount();
    vi.mocked(api.get).mockClear();
    mount(limited(), { trial: false });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    await waitFor(() => expect(vi.mocked(api.get).mock.results.length).toBeGreaterThan(0));
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(screen.queryByRole('region', { name: 'Verify your account' })).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/trust/');
  });

  test('an unreadable status shows nothing rather than a button that may not work', async () => {
    mount('unreachable');
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/trust/'));
    expect(screen.queryByRole('region', { name: 'Verify your account' })).not.toBeInTheDocument();
  });

  test('the button waits for a verified email and for GitHub sign-in to be set up', async () => {
    const { unmount } = mount(limited({ emailVerified: false }));
    expect(await screen.findByRole('button', { name: 'Verify with GitHub' })).toBeDisabled();
    expect(screen.getByText(/Verify your email address first/)).toBeInTheDocument();
    unmount();
    mount(limited({ githubConfigured: false }));
    expect(await screen.findByRole('button', { name: 'Verify with GitHub' })).toBeDisabled();
    expect(screen.getByText(/not set up on this platform/)).toBeInTheDocument();
  });

  test('the button is disabled during the wait between two checks', async () => {
    mount(limited({ retryAfterSeconds: 300 }));
    expect(await screen.findByRole('button', { name: 'Verify with GitHub' })).toBeDisabled();
  });

  test('says what happened on the way back from GitHub', async () => {
    mount(limited(), { route: '/?verify=cancelled' });
    expect(await screen.findByRole('status')).toHaveTextContent('cancelled');
  });

  test('shows the server refusal when the start fails', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { status: 429, data: { error: 'try again in 7 minutes' } } });
    mount(limited());
    await userEvent.click(await screen.findByRole('button', { name: 'Verify with GitHub' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('try again in 7 minutes');
    expect(assign).not.toHaveBeenCalled();
  });
});
