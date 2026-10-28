import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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
  guestSignInEnabled: true,
  guestCaptchaSecretSet: false,
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

describe('guest sign-in', () => {
  test('stays hidden while the authguest feature is off', async () => {
    serve({ authoauth: true, authotp: true });
    renderPage();
    await screen.findByTestId('redirect-urls-card');
    expect(screen.queryByTestId('guest-sign-in-card')).not.toBeInTheDocument();
  });

  test('shows with authguest alone, beside the new-user switch it obeys', async () => {
    serve({ authguest: true });
    renderPage();
    const card = await screen.findByTestId('guest-sign-in-card');
    expect(within(card).getByLabelText(/guest accounts/i)).toBeChecked();
    expect(screen.getByTestId('redirect-urls-card')).toBeInTheDocument();
    expect(screen.queryByTestId('email-sign-in-card')).not.toBeInTheDocument();
    expect(screen.queryByTestId('auth-providers')).not.toBeInTheDocument();
  });
});
