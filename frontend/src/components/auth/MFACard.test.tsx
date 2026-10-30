import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MFACard } from './MFACard';
import { api } from '../../api/client';
import type { AuthSettings } from '../../api/authSettings';
import { useProjectRole } from '../../hooks/useProjectRole';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));
vi.mock('../../hooks/useProjectRole', () => ({ useProjectRole: vi.fn() }));

const asRole = (canAdmin: boolean) =>
  vi.mocked(useProjectRole).mockReturnValue({ role: canAdmin ? 'admin' : 'developer', canDevelop: true, canAdmin, isLoading: false });

const base: AuthSettings = { siteUrl: 'https://app.example.com', requireEmailVerification: false };

function renderCard(settings: AuthSettings) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  client.setQueryData(['auth-settings', 'p'], settings);
  render(<QueryClientProvider client={client}><MFACard projectId="p" settings={settings} /></QueryClientProvider>);
  return client;
}

beforeEach(() => {
  vi.resetAllMocks();
  asRole(true);
});

describe('MFACard', () => {
  test('an answer without the field reads as off', () => {
    renderCard(base);
    expect(screen.getByLabelText(/authenticator apps/i)).not.toBeChecked();
  });

  test('explains factors, recovery codes and the aal2 permission check', () => {
    renderCard(base);
    const card = screen.getByTestId('mfa-card');
    expect(card).toHaveTextContent(/authenticator app/i);
    expect(card).toHaveTextContent(/recovery codes/i);
    expect(card).toHaveTextContent('x-excalibase-aal');
    expect(card).toHaveTextContent('aal2');
  });

  test('switching on sends only the switch to its own path and keeps the answer', async () => {
    const u = userEvent.setup();
    const answer = { ...base, mfaEnabled: true };
    vi.mocked(api.put).mockResolvedValue({ data: answer });
    const client = renderCard(base);
    await u.click(screen.getByLabelText(/authenticator apps/i));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p/auth-settings/mfa', { enabled: true }));
    await waitFor(() => expect(client.getQueryData(['auth-settings', 'p'])).toEqual(answer));
  });

  test('switching off asks first and says enrolled factors are kept', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockResolvedValue({ data: { ...base, mfaEnabled: false } });
    renderCard({ ...base, mfaEnabled: true });
    expect(screen.getByTestId('mfa-card')).toHaveTextContent(/kept/i);
    await u.click(screen.getByLabelText(/authenticator apps/i));
    expect(screen.getByTestId('confirm-modal')).toHaveTextContent(/second step/i);
    expect(api.put).not.toHaveBeenCalled();
    await u.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/p/auth-settings/mfa', { enabled: false }));
  });

  test('cancelling the switch-off sends nothing', async () => {
    const u = userEvent.setup();
    renderCard({ ...base, mfaEnabled: true });
    await u.click(screen.getByLabelText(/authenticator apps/i));
    await u.click(screen.getByTestId('modal-cancel'));
    expect(api.put).not.toHaveBeenCalled();
    expect(screen.getByLabelText(/authenticator apps/i)).toBeChecked();
  });

  test('only project admins can switch it', () => {
    asRole(false);
    renderCard(base);
    expect(screen.getByLabelText(/authenticator apps/i)).toBeDisabled();
    expect(screen.getByTestId('mfa-card')).toHaveTextContent(/only project admins/i);
  });

  test("shows the server's refusal", async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockRejectedValue({ response: { status: 409, data: { error: "the project's MFA key in the vault is not a valid key" } } });
    renderCard(base);
    await u.click(screen.getByLabelText(/authenticator apps/i));
    expect(await screen.findByRole('alert')).toHaveTextContent(/not a valid key/);
  });
});
