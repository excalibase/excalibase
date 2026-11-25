import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AllowNewUsersToggle } from './AllowNewUsersToggle';
import type { AuthSettings } from '../../api/authSettings';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const base: AuthSettings = { siteUrl: 'https://app.example.com', requireEmailVerification: true };

function renderToggle(settings: AuthSettings, extra?: Partial<AuthSettings>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><AllowNewUsersToggle projectId="p" settings={settings} extra={extra} /></QueryClientProvider>);
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('AllowNewUsersToggle', () => {
  test('reads an absent value as allowed and a stored false as unchecked', () => {
    const { unmount } = renderToggle(base);
    expect(screen.getByLabelText(/allow new users/i)).toBeChecked();
    unmount();
    renderToggle({ ...base, allowNewUsers: false });
    expect(screen.getByLabelText(/allow new users/i)).not.toBeChecked();
  });

  test('saves the switch with the settings and the host extras', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockImplementation(async (_path: string, body: unknown) => ({ data: body }));
    renderToggle({ ...base, allowNewUsers: true }, { redirectUrls: ['https://a.example.com/cb'] });
    await u.click(screen.getByLabelText(/allow new users/i));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p/auth-settings', {
      siteUrl: base.siteUrl, requireEmailVerification: true, redirectUrls: ['https://a.example.com/cb'], allowNewUsers: false,
    }));
  });

  test('disables the checkbox while saving and reports a failed save', async () => {
    const u = userEvent.setup();
    let reject: (e: Error) => void = () => {};
    vi.mocked(api.put).mockImplementation(() => new Promise((_, r) => { reject = r; }));
    renderToggle(base);
    await u.click(screen.getByLabelText(/allow new users/i));
    await waitFor(() => expect(screen.getByLabelText(/allow new users/i)).toBeDisabled());
    reject(new Error('boom'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not save/i);
    expect(screen.getByLabelText(/allow new users/i)).toBeEnabled();
  });
});
