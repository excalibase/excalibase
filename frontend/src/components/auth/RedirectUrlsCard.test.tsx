import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RedirectUrlsCard } from './RedirectUrlsCard';
import { redirectUrlProblem } from '../../api/authSettings';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

function renderCard(settings: { siteUrl: string; requireEmailVerification: boolean; redirectUrls?: string[]; allowNewUsers?: boolean }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><RedirectUrlsCard projectId="p" settings={settings} /></QueryClientProvider>);
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('redirectUrlProblem', () => {
  test('needs a value and follows the site URL rule', () => {
    expect(redirectUrlProblem(' ')).toContain('Enter');
    expect(redirectUrlProblem('https://app.example.com/cb')).toBeNull();
    expect(redirectUrlProblem('https://app.example.com/cb?x=1')).toContain('query');
  });
});

describe('RedirectUrlsCard', () => {
  test('an older settings answer reads as an empty list with new users allowed', () => {
    renderCard({ siteUrl: '', requireEmailVerification: false });
    expect(screen.getByTestId('redirect-urls-empty')).toBeInTheDocument();
    expect(screen.getByLabelText(/allow new users/i)).toBeChecked();
  });

  test("shows the server's refusal", async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockRejectedValue({ response: { status: 400, data: { error: 'invalid redirect urls: at most 20 entries' } } });
    renderCard({ siteUrl: '', requireEmailVerification: false, redirectUrls: [], allowNewUsers: true });
    await u.type(screen.getByLabelText('Redirect URL'), 'https://app.example.com/cb');
    await u.click(screen.getByRole('button', { name: /add url/i }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('at most 20 entries'));
    expect(screen.getByLabelText('Redirect URL')).toHaveValue('https://app.example.com/cb');
  });
});
