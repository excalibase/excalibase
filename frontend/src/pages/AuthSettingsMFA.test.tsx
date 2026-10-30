import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthSettingsPage } from './AuthSettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const SETTINGS_PATH = '/projects/proj-1/auth-settings';

function serve(features: Record<string, boolean>) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path === '/config') return { data: { deploymentMode: 'selfhosted', features } };
    if (path === SETTINGS_PATH) return { data: { siteUrl: 'https://app.example.com', requireEmailVerification: false, mfaEnabled: true } };
    if (path === '/projects/proj-1/auth-providers') return { data: { providers: [] } };
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

describe('multi-factor authentication', () => {
  test('stays hidden while the authmfa feature is off', async () => {
    serve({ authoauth: true, authguest: true });
    renderPage();
    await screen.findByTestId('guest-sign-in-card');
    expect(screen.queryByTestId('mfa-card')).not.toBeInTheDocument();
  });

  test('shows with authmfa alone, without the sign-in cards', async () => {
    serve({ authmfa: true });
    renderPage();
    const card = await screen.findByTestId('mfa-card');
    expect(card).toHaveTextContent(/multi-factor authentication/i);
    expect(screen.getByLabelText(/authenticator apps/i)).toBeChecked();
    expect(screen.queryByTestId('redirect-urls-card')).not.toBeInTheDocument();
  });
});
