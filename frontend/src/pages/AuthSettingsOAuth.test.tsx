import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthSettingsPage } from './AuthSettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const SETTINGS_PATH = '/projects/proj-1/auth-settings';
const PROVIDERS_PATH = '/projects/proj-1/auth-providers';
const GITHUB_CALLBACK = 'https://api.example.com/auth/acme/proj-1/callback/github';

const providers = [
  { provider: 'google', enabled: false, clientId: '', clientSecretSet: false, callbackUrl: 'https://api.example.com/auth/acme/proj-1/callback/google' },
  { provider: 'github', enabled: true, clientId: 'gh-client', clientSecretSet: true, callbackUrl: GITHUB_CALLBACK },
];

const settings = {
  siteUrl: 'https://app.example.com',
  requireEmailVerification: true,
  redirectUrls: ['https://app.example.com/auth/callback'],
  allowNewUsers: true,
};

function serve(flagOn: boolean) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path === '/config') return { data: { deploymentMode: 'selfhosted', features: { authoauth: flagOn } } };
    if (path === SETTINGS_PATH) return { data: settings };
    if (path === PROVIDERS_PATH) return { data: { providers } };
    return { data: [] };
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/auth/settings']}>
        <Routes>
          <Route path="/project/:projectId/auth/settings" element={<AuthSettingsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe('end-user sign-in settings', () => {
  test('stay hidden and unrequested while the authoauth feature is off', async () => {
    serve(false);
    renderPage();
    await screen.findByTestId('auth-settings-form');
    expect(screen.queryByTestId('auth-providers')).not.toBeInTheDocument();
    expect(screen.queryByTestId('redirect-urls-card')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(PROVIDERS_PATH);
  });

  test('show each provider with its callback URL and the secret only as set', async () => {
    serve(true);
    renderPage();
    const github = await screen.findByTestId('auth-provider-github');
    expect(github).toHaveTextContent(GITHUB_CALLBACK);
    expect(within(github).getByLabelText('Client ID')).toHaveValue('gh-client');
    const secret = within(github).getByLabelText('Client secret');
    expect(secret).toHaveValue('');
    expect(secret).toHaveAttribute('type', 'password');
    expect(within(github).getByTestId('auth-provider-github-secret-state')).toHaveTextContent(/set/i);
    expect(within(screen.getByTestId('auth-provider-google')).getByTestId('auth-provider-google-secret-state')).toHaveTextContent(/not set/i);
    expect(within(github).getByRole('link', { name: /github developer settings/i })).toHaveAttribute('href', 'https://github.com/settings/developers');
    expect(within(screen.getByTestId('auth-provider-google')).getByRole('link', { name: /google cloud console/i }))
      .toHaveAttribute('href', 'https://console.cloud.google.com/apis/credentials');
  });

  test('copy the callback URL', async () => {
    // userEvent.setup() installs its own clipboard, so the spy goes in after it.
    const u = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    serve(true);
    renderPage();
    const github = await screen.findByTestId('auth-provider-github');
    await u.click(within(github).getByRole('button', { name: /copy callback url/i }));
    expect(writeText).toHaveBeenCalledWith(GITHUB_CALLBACK);
  });

  test('send the secret only when one is typed', async () => {
    const u = userEvent.setup();
    serve(true);
    vi.mocked(api.put).mockResolvedValue({ data: providers[1] });
    renderPage();
    const github = await screen.findByTestId('auth-provider-github');
    await u.clear(within(github).getByLabelText('Client ID'));
    await u.type(within(github).getByLabelText('Client ID'), 'gh-client-2');
    await u.click(within(github).getByRole('button', { name: 'Save GitHub' }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(`${PROVIDERS_PATH}/github`, { enabled: true, clientId: 'gh-client-2' }));

    const google = screen.getByTestId('auth-provider-google');
    await u.type(within(google).getByLabelText('Client ID'), 'g-client');
    await u.type(within(google).getByLabelText('Client secret'), 'g-secret');
    await u.click(within(google).getByLabelText('Enable Google'));
    await u.click(within(google).getByRole('button', { name: 'Save Google' }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(`${PROVIDERS_PATH}/google`,
      { enabled: true, clientId: 'g-client', clientSecret: 'g-secret' }));
    await waitFor(() => expect(within(google).getByLabelText('Client secret')).toHaveValue(''));
  });

  test("show the server's refusal", async () => {
    const u = userEvent.setup();
    serve(true);
    vi.mocked(api.put).mockRejectedValue({ response: { status: 403, data: { error: 'insufficient project role' } } });
    renderPage();
    const github = await screen.findByTestId('auth-provider-github');
    await u.click(within(github).getByRole('button', { name: 'Save GitHub' }));
    expect(await within(github).findByRole('alert')).toHaveTextContent('insufficient project role');
  });

  test('add and remove redirect URLs with the whole settings body', async () => {
    const u = userEvent.setup();
    serve(true);
    vi.mocked(api.put).mockImplementation(async (_path: string, body: unknown) => ({ data: body }));
    renderPage();
    const card = await screen.findByTestId('redirect-urls-card');
    await u.type(within(card).getByLabelText('Redirect URL'), 'http://localhost:3000/auth/callback');
    await u.click(within(card).getByRole('button', { name: /add url/i }));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SETTINGS_PATH, {
      siteUrl: settings.siteUrl,
      requireEmailVerification: true,
      redirectUrls: ['https://app.example.com/auth/callback', 'http://localhost:3000/auth/callback'],
      allowNewUsers: true,
    }));

    await u.click(within(card).getByRole('button', { name: 'Remove https://app.example.com/auth/callback' }));
    await waitFor(() => expect(api.put).toHaveBeenLastCalledWith(SETTINGS_PATH, expect.objectContaining({
      redirectUrls: ['http://localhost:3000/auth/callback'],
    })));
  });

  test('refuse a redirect URL that is not absolute before asking the server', async () => {
    const u = userEvent.setup();
    serve(true);
    renderPage();
    const card = await screen.findByTestId('redirect-urls-card');
    await u.type(within(card).getByLabelText('Redirect URL'), 'app.example.com/cb');
    await u.click(within(card).getByRole('button', { name: /add url/i }));
    expect(await within(card).findByRole('alert')).toHaveTextContent(/absolute/i);
    expect(api.put).not.toHaveBeenCalled();
  });

  test('switch off new users', async () => {
    const u = userEvent.setup();
    serve(true);
    vi.mocked(api.put).mockImplementation(async (_path: string, body: unknown) => ({ data: body }));
    renderPage();
    const card = await screen.findByTestId('redirect-urls-card');
    await u.click(within(card).getByLabelText(/allow new users/i));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SETTINGS_PATH, expect.objectContaining({
      allowNewUsers: false, redirectUrls: settings.redirectUrls,
    })));
  });
});
