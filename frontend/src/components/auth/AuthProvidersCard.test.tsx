import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthProvidersCard } from './AuthProvidersCard';
import { providerUpdateBody } from '../../api/authProviders';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const unconfigured = { provider: 'github', enabled: false, clientId: '', clientSecretSet: false, callbackUrl: 'https://api.x/auth/o/p/callback/github' };

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><AuthProvidersCard projectId="p" /></QueryClientProvider>);
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('providerUpdateBody', () => {
  test('trims the client id and leaves a blank secret out', () => {
    expect(providerUpdateBody(false, ' id ', '')).toEqual({ enabled: false, clientId: 'id' });
    expect(providerUpdateBody(true, 'id', ' s ')).toEqual({ enabled: true, clientId: 'id', clientSecret: ' s ' });
  });
});

describe('AuthProvidersCard', () => {
  test('says why the providers are missing when they cannot be read', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { status: 503, data: { error: 'sign-in providers are not available' } } });
    renderCard();
    expect(await screen.findByRole('alert')).toHaveTextContent('sign-in providers are not available');
  });

  test('after a save the secret reads as set and the field is empty again', async () => {
    const u = userEvent.setup();
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [unconfigured] } });
    vi.mocked(api.put).mockResolvedValue({ data: { ...unconfigured, clientId: 'gh', clientSecretSet: true } });
    renderCard();
    const github = await screen.findByTestId('auth-provider-github');
    await u.type(within(github).getByLabelText('Client ID'), 'gh');
    await u.type(within(github).getByLabelText('Client secret'), 'shh');
    await u.click(within(github).getByRole('button', { name: 'Save GitHub' }));
    await waitFor(() => expect(within(github).getByTestId('auth-provider-github-secret-state')).toHaveTextContent('Secret set'));
    expect(within(github).getByLabelText('Client secret')).toHaveValue('');
    expect(within(github).getByText(/^Saved/)).toBeInTheDocument();
  });

  test('removing the secret asks first, then turns the provider off and empties it', async () => {
    const u = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const configured = { ...unconfigured, enabled: true, clientId: 'gh', clientSecretSet: true };
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [configured] } });
    vi.mocked(api.delete).mockResolvedValue({ data: unconfigured });
    renderCard();
    const github = await screen.findByTestId('auth-provider-github');
    await u.click(within(github).getByRole('button', { name: 'Remove GitHub secret' }));
    expect(window.confirm).toHaveBeenCalled();
    expect(api.delete).toHaveBeenCalledWith('/projects/p/auth-providers/github');
    await waitFor(() => expect(within(github).getByTestId('auth-provider-github-secret-state')).toHaveTextContent('Secret not set'));
    expect(within(github).getByLabelText('Enable GitHub')).not.toBeChecked();
    expect(within(github).getByLabelText('Client ID')).toHaveValue('');
  });

  test('declining keeps the secret and no remove button shows without one', async () => {
    const u = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    vi.mocked(api.get).mockResolvedValue({ data: { providers: [{ ...unconfigured, clientSecretSet: true }, { ...unconfigured, provider: 'google' }] } });
    renderCard();
    const github = await screen.findByTestId('auth-provider-github');
    await u.click(within(github).getByRole('button', { name: 'Remove GitHub secret' }));
    expect(api.delete).not.toHaveBeenCalled();
    expect(within(screen.getByTestId('auth-provider-google')).queryByRole('button', { name: /Remove/ })).toBeNull();
  });
});
