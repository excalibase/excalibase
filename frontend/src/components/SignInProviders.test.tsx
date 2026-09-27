import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SignInProviders } from './SignInProviders';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const listed = {
  providers: [
    { provider: 'google', enabled: false, clientId: '', clientSecretSet: false, callbackUrl: 'https://studio.x/api/auth/oauth/google/callback' },
    { provider: 'github', enabled: true, clientId: 'gh-client', clientSecretSet: true, callbackUrl: 'https://studio.x/api/auth/oauth/github/callback' },
  ],
};

describe('Studio sign-in providers settings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: listed } as never);
  });

  test('shows each provider with the callback URL to register and the secret only as set', async () => {
    render(<SignInProviders />);
    const github = await screen.findByTestId('sso-github');
    expect(github).toHaveTextContent('https://studio.x/api/auth/oauth/github/callback');
    expect(within(github).getByText(/secret is set/i)).toBeInTheDocument();
    expect(within(github).getByLabelText('Client ID')).toHaveValue('gh-client');
    expect(within(github).getByLabelText('Client secret')).toHaveValue('');
    expect(within(screen.getByTestId('sso-google')).getByText(/no secret set/i)).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/admin/sso-providers/');
  });

  test('saving sends the secret only when one is typed', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockResolvedValue({ data: listed.providers[0] } as never);
    render(<SignInProviders />);
    const google = await screen.findByTestId('sso-google');

    await u.type(within(google).getByLabelText('Client ID'), 'g-client');
    await u.type(within(google).getByLabelText('Client secret'), 'g-secret');
    await u.click(within(google).getByLabelText('Enabled'));
    await u.click(within(google).getByRole('button', { name: /save/i }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/admin/sso-providers/google',
      { enabled: true, clientId: 'g-client', clientSecret: 'g-secret' }));

    const github = screen.getByTestId('sso-github');
    await u.click(within(github).getByLabelText('Enabled'));
    await u.click(within(github).getByRole('button', { name: /save/i }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/admin/sso-providers/github',
      { enabled: false, clientId: 'gh-client' }));
  });

  test('a refused save is reported', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockRejectedValue({ response: { data: { error: 'enabling a provider needs its client id and client secret' } } });
    render(<SignInProviders />);
    const google = await screen.findByTestId('sso-google');
    await u.click(within(google).getByLabelText('Enabled'));
    await u.click(within(google).getByRole('button', { name: /save/i }));
    expect(await within(google).findByText(/needs its client id/i)).toBeInTheDocument();
  });

  test('a list that cannot be loaded is reported', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('network'));
    render(<SignInProviders />);
    expect(await screen.findByText(/could not load the sign-in providers/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /save/i })).not.toBeInTheDocument();
  });

  test('a saved provider confirms and shows its secret as set', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockResolvedValue({ data: { ...listed.providers[0], enabled: true, clientId: 'g-client', clientSecretSet: true } } as never);
    render(<SignInProviders />);
    const google = await screen.findByTestId('sso-google');
    await u.type(within(google).getByLabelText('Client secret'), 'g-secret');
    await u.click(within(google).getByRole('button', { name: /save/i }));
    expect(await within(google).findByText(/applies to the next sign-in/i)).toBeInTheDocument();
    expect(within(google).getByText(/secret is set/i)).toBeInTheDocument();
    expect(within(google).getByLabelText('Client secret')).toHaveValue('');
  });

  test('a save refused without a reason gets a generic message', async () => {
    const u = userEvent.setup();
    vi.mocked(api.put).mockRejectedValue(new Error('network'));
    render(<SignInProviders />);
    const github = await screen.findByTestId('sso-github');
    await u.click(within(github).getByRole('button', { name: /save/i }));
    expect(await within(github).findByText('Could not save the provider')).toBeInTheDocument();
  });

  test('a provider without a display name is shown by its id', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [{ ...listed.providers[0], provider: 'gitlab' }] } } as never);
    render(<SignInProviders />);
    const gitlab = await screen.findByTestId('sso-gitlab');
    expect(within(gitlab).getByRole('heading', { name: 'gitlab' })).toBeInTheDocument();
  });
});
