import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { GuestSignInCard } from './GuestSignInCard';
import { api } from '../../api/client';
import type { AuthSettings } from '../../api/authSettings';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const base: AuthSettings = { siteUrl: 'https://app.example.com', requireEmailVerification: false };

function renderCard(settings: AuthSettings) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><GuestSignInCard projectId="p" settings={settings} /></QueryClientProvider>);
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('GuestSignInCard', () => {
  test('an older settings answer reads as off with no captcha', () => {
    renderCard(base);
    expect(screen.getByLabelText(/guest accounts/i)).not.toBeChecked();
    expect(screen.getByTestId('guest-captcha-state')).toHaveTextContent(/no turnstile secret/i);
  });

  test('says a guest reaches nothing until the guest role has permissions', () => {
    renderCard(base);
    expect(screen.getByText(/role guest/i)).toBeInTheDocument();
    expect(screen.getByText(/reaches nothing until/i)).toBeInTheDocument();
  });

  test('turning it on sends only the switch with the site settings', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockImplementation(async (_path: string, body: unknown) => ({ data: body }));
    renderCard(base);
    await u.click(screen.getByLabelText(/guest accounts/i));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p/auth-settings', {
      siteUrl: base.siteUrl, requireEmailVerification: false, guestSignInEnabled: true,
    }));
  });

  test('saves a Turnstile secret, clears the box and never shows it again', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockResolvedValue({ data: { guestSignInEnabled: true, guestCaptchaSecretSet: true } });
    renderCard({ ...base, guestSignInEnabled: true, guestCaptchaSecretSet: false });
    const input = screen.getByLabelText(/turnstile secret key/i);
    expect(input).toHaveAttribute('type', 'password');
    await u.type(input, '0x4AAAAAAAsecret');
    await u.click(screen.getByRole('button', { name: /save secret/i }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p/auth-settings/guest-captcha', { secretKey: '0x4AAAAAAAsecret' }));
    await waitFor(() => expect(input).toHaveValue(''));
  });

  test('removes the secret', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: { guestSignInEnabled: true, guestCaptchaSecretSet: false } });
    renderCard({ ...base, guestSignInEnabled: true, guestCaptchaSecretSet: true });
    expect(screen.getByTestId('guest-captcha-state')).toHaveTextContent(/turnstile is required/i);
    await u.click(screen.getByRole('button', { name: /remove secret/i }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/p/auth-settings/guest-captcha'));
  });

  test("shows the server's refusal", async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockRejectedValue({ response: { status: 400, data: { error: 'the Turnstile secret key must be 1 to 256 printable characters without spaces' } } });
    renderCard(base);
    await u.type(screen.getByLabelText(/turnstile secret key/i), 'x');
    await u.click(screen.getByRole('button', { name: /save secret/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/printable characters/);
  });
});
