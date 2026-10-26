import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { EmailSignInCard } from './EmailSignInCard';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

function renderCard(settings: { siteUrl: string; requireEmailVerification: boolean; emailOtpEnabled?: boolean }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><EmailSignInCard projectId="p" settings={settings} /></QueryClientProvider>);
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('EmailSignInCard', () => {
  test('an older settings answer reads as off', () => {
    renderCard({ siteUrl: '', requireEmailVerification: false });
    expect(screen.getByLabelText(/email links and codes/i)).not.toBeChecked();
  });

  test('tells the admin codes are eight digits', () => {
    renderCard({ siteUrl: '', requireEmailVerification: false });
    expect(screen.getByText(/eight-digit code/i)).toBeInTheDocument();
  });

  test('turning it off sends only the switch with the site settings', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockImplementation(async (_path: string, body: unknown) => ({ data: body }));
    renderCard({ siteUrl: 'https://app.example.com', requireEmailVerification: true, emailOtpEnabled: true });
    await u.click(screen.getByLabelText(/email links and codes/i));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p/auth-settings', {
      siteUrl: 'https://app.example.com', requireEmailVerification: true, emailOtpEnabled: false,
    }));
  });
});
