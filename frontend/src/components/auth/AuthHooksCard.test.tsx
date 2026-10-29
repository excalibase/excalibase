import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthHooksCard } from './AuthHooksCard';
import { api } from '../../api/client';
import type { AuthHooks } from '../../api/authHooks';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const HOOKS_PATH = '/projects/p/auth-hooks';
const SECRET = `whsec_${'fixture'.repeat(6)}`;

function hooksWith(secretSet: boolean, enabled: Partial<Record<string, string>> = {}): AuthHooks {
  return {
    secretSet,
    hooks: ['custom_access_token', 'before_user_created', 'send_email'].map((hook) => ({
      hook: hook as AuthHooks['hooks'][number]['hook'],
      enabled: hook in enabled,
      url: enabled[hook] ?? '',
    })),
  };
}

function renderCard(initial: AuthHooks) {
  vi.mocked(api.get).mockResolvedValue({ data: initial });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><AuthHooksCard projectId="p" /></QueryClientProvider>);
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('AuthHooksCard', () => {
  test('lists the three hooks with what each does', async () => {
    renderCard(hooksWith(false));
    expect(await screen.findByTestId('auth-hook-custom_access_token')).toHaveTextContent(/claims to every access token/i);
    expect(screen.getByTestId('auth-hook-custom_access_token')).toHaveTextContent(/default role among the user's allowed roles/i);
    expect(screen.getByTestId('auth-hook-before_user_created')).toHaveTextContent(/allows or denies each new account/i);
    expect(screen.getByTestId('auth-hook-send_email')).toHaveTextContent(/your own mailer/i);
    expect(screen.getByTestId('auth-hook-send_email')).toHaveTextContent(/port 443 only/i);
    expect(api.get).toHaveBeenCalledWith(HOOKS_PATH);
  });

  test('explains how to verify a call and that a failing hook fails sign-in', async () => {
    renderCard(hooksWith(false));
    const card = await screen.findByTestId('auth-hooks-card');
    expect(card).toHaveTextContent(/Standard Webhooks/);
    expect(card).toHaveTextContent(/webhook-id/);
    expect(card).toHaveTextContent(/webhook-timestamp/);
    expect(card).toHaveTextContent(/webhook-signature/);
    expect(card).toHaveTextContent(/more than 5 minutes/i);
    expect(card).toHaveTextContent(/over 2 s/i);
  });

  test('without a secret a hook cannot be switched on, but its URL can be saved', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockResolvedValue({ data: hooksWith(false) });
    renderCard(hooksWith(false));
    expect(await screen.findByTestId('auth-hooks-secret-state')).toHaveTextContent('Not set');
    const form = screen.getByTestId('auth-hook-send_email');
    expect(within(form).getByLabelText(/enable send email/i)).toBeDisabled();
    expect(within(form).getByText(/generate a signing secret first/i)).toBeInTheDocument();
    await u.type(within(form).getByLabelText(/send email url/i), 'https://mail.example.com/send');
    await u.click(within(form).getByRole('button', { name: /save send email/i }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(`${HOOKS_PATH}/send_email`, { enabled: false, url: 'https://mail.example.com/send' }));
  });

  test('switches a hook on with its URL', async () => {
    const u = userEvent.setup();
    const saved = hooksWith(true, { custom_access_token: 'https://hooks.example.com/claims' });
    vi.mocked(api.put).mockResolvedValue({ data: saved });
    renderCard(hooksWith(true));
    const form = await screen.findByTestId('auth-hook-custom_access_token');
    await u.click(within(form).getByLabelText(/enable custom access token/i));
    await u.type(within(form).getByLabelText(/custom access token url/i), ' https://hooks.example.com/claims ');
    await u.click(within(form).getByRole('button', { name: /save custom access token/i }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(`${HOOKS_PATH}/custom_access_token`, {
      enabled: true, url: 'https://hooks.example.com/claims',
    }));
    expect(await within(screen.getByTestId('auth-hook-custom_access_token')).findByText(/saved/i)).toBeInTheDocument();
  });

  test("shows the server's refusal", async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockRejectedValue({ response: { status: 400, data: { error: 'the hook URL must be an https:// URL' } } });
    renderCard(hooksWith(true));
    const form = await screen.findByTestId('auth-hook-before_user_created');
    await u.type(within(form).getByLabelText(/before user created url/i), 'http://x');
    await u.click(within(form).getByRole('button', { name: /save before user created/i }));
    expect(await within(form).findByRole('alert')).toHaveTextContent('https:// URL');
  });

  test('generates the secret and shows it once with a copy button and a warning', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { secret: SECRET } });
    renderCard(hooksWith(false));
    await u.click(await screen.findByRole('button', { name: /generate secret/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(`${HOOKS_PATH}/secret`));
    const issued = await screen.findByTestId('auth-hooks-new-secret');
    expect(issued).toHaveTextContent(SECRET);
    expect(issued).toHaveTextContent(/not shown again/i);
    expect(within(issued).getByRole('button', { name: /copy/i })).toBeInTheDocument();
    expect(screen.getByTestId('auth-hooks-secret-state')).toHaveTextContent('Set');
    expect(screen.getByRole('button', { name: /rotate secret/i })).toBeInTheDocument();
    expect(within(screen.getByTestId('auth-hook-send_email')).getByLabelText(/enable send email/i)).toBeEnabled();
  });

  test('rotating asks first, since the hook breaks until the endpoint uses the new secret', async () => {
    const u = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    vi.mocked(api.post).mockResolvedValue({ data: { secret: SECRET } });
    renderCard(hooksWith(true));
    const rotate = await screen.findByRole('button', { name: /rotate secret/i });
    await u.click(rotate);
    expect(confirm.mock.calls[0][0]).toMatch(/fail until your endpoint uses the new secret/i);
    expect(api.post).not.toHaveBeenCalled();
    await u.click(rotate);
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
    expect(await screen.findByTestId('auth-hooks-new-secret')).toHaveTextContent(/rotating breaks/i);
  });

  test('removing the secret switches the hooks off', async () => {
    const u = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const switchedOff = hooksWith(false);
    switchedOff.hooks[2].url = 'https://mail.example.com';
    vi.mocked(api.delete).mockResolvedValue({ data: switchedOff });
    renderCard(hooksWith(true, { send_email: 'https://mail.example.com' }));
    const form = await screen.findByTestId('auth-hook-send_email');
    expect(within(form).getByLabelText(/enable send email/i)).toBeChecked();
    await u.click(screen.getByRole('button', { name: /remove secret/i }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${HOOKS_PATH}/secret`));
    await waitFor(() => expect(within(screen.getByTestId('auth-hook-send_email')).getByLabelText(/enable send email/i)).not.toBeChecked());
    expect(screen.getByTestId('auth-hooks-secret-state')).toHaveTextContent('Not set');
  });

  test('says why the hooks are missing when they cannot be read', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { status: 503, data: { error: 'auth hooks are not available' } } });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AuthHooksCard projectId="p" /></QueryClientProvider>);
    expect(await screen.findByRole('alert')).toHaveTextContent('auth hooks are not available');
  });
});
