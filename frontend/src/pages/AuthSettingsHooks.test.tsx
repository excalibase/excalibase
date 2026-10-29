import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthSettingsPage } from './AuthSettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const SETTINGS_PATH = '/projects/proj-1/auth-settings';
const HOOKS_PATH = '/projects/proj-1/auth-hooks';

function serve(features: Record<string, boolean>) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path === '/config') return { data: { deploymentMode: 'selfhosted', features } };
    if (path === SETTINGS_PATH) return { data: { siteUrl: 'https://app.example.com', requireEmailVerification: false } };
    if (path === HOOKS_PATH) {
      return { data: { secretSet: false, hooks: [
        { hook: 'custom_access_token', enabled: false, url: '' },
        { hook: 'before_user_created', enabled: false, url: '' },
        { hook: 'send_email', enabled: false, url: '' },
      ] } };
    }
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

describe('auth hooks', () => {
  test('stay hidden, and unread, while the authhooks feature is off', async () => {
    serve({ authoauth: true, authotp: true, authguest: true, authuser: true });
    renderPage();
    await screen.findByTestId('redirect-urls-card');
    expect(screen.queryByTestId('auth-hooks-card')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(HOOKS_PATH);
  });

  test('show with authhooks alone, without the sign-in method cards', async () => {
    serve({ authhooks: true });
    renderPage();
    expect(await screen.findByTestId('auth-hook-send_email')).toBeInTheDocument();
    expect(screen.getByTestId('auth-hooks-card')).toBeInTheDocument();
    expect(screen.queryByTestId('redirect-urls-card')).not.toBeInTheDocument();
    expect(screen.queryByTestId('guest-sign-in-card')).not.toBeInTheDocument();
  });
});
