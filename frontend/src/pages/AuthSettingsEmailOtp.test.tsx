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

const settings = {
  siteUrl: 'https://app.example.com',
  requireEmailVerification: false,
  redirectUrls: ['https://app.example.com/auth/callback'],
  allowNewUsers: true,
  emailOtpEnabled: false,
};

function serve(features: Record<string, boolean>) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path === '/config') return { data: { deploymentMode: 'selfhosted', features } };
    if (path === SETTINGS_PATH) return { data: settings };
    if (path === PROVIDERS_PATH) return { data: { providers: [] } };
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

describe('sign-in by emailed link or code', () => {
  test('stays hidden while the authotp feature is off, even with authoauth on', async () => {
    serve({ authoauth: true, authotp: false });
    renderPage();
    await screen.findByTestId('redirect-urls-card');
    expect(screen.queryByTestId('email-sign-in-card')).not.toBeInTheDocument();
  });

  test('shows its switch and the redirect URLs, not the providers, with authotp alone', async () => {
    serve({ authoauth: false, authotp: true });
    renderPage();
    const card = await screen.findByTestId('email-sign-in-card');
    expect(within(card).getByLabelText(/email links and codes/i)).not.toBeChecked();
    expect(screen.getByTestId('redirect-urls-card')).toBeInTheDocument();
    expect(screen.queryByTestId('auth-providers')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(PROVIDERS_PATH);
  });

  test('turns on with the site settings and leaves the redirect list to the server', async () => {
    const u = userEvent.setup();
    serve({ authotp: true });
    vi.mocked(api.put).mockImplementation(async (_path: string, body: unknown) => ({ data: { ...settings, ...(body as object) } }));
    renderPage();
    const card = await screen.findByTestId('email-sign-in-card');
    await u.click(within(card).getByLabelText(/email links and codes/i));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SETTINGS_PATH, {
      siteUrl: settings.siteUrl, requireEmailVerification: false, emailOtpEnabled: true,
    }));
  });

  test("shows the server's refusal", async () => {
    const u = userEvent.setup();
    serve({ authotp: true });
    vi.mocked(api.put).mockRejectedValue({ response: { status: 403, data: { error: 'insufficient project role' } } });
    renderPage();
    const card = await screen.findByTestId('email-sign-in-card');
    await u.click(within(card).getByLabelText(/email links and codes/i));
    expect(await within(card).findByRole('alert')).toHaveTextContent('insufficient project role');
  });
});
