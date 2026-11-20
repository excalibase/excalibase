import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SiteFormPage } from './SiteFormPage';
import { api } from '../api/client';
import { developerRole, signInAsMember, siteFixture } from '../test/siteFixtures';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function renderPage(features: Record<string, boolean> = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', sites: true, features } } as never);
    if (url === '/projects/proj-1/sites/') return Promise.resolve({ data: [] } as never);
    const role = developerRole(url);
    if (role) return role;
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/sites/new']}>
        <Routes>
          <Route path="/project/:projectId/sites/new" element={<SiteFormPage />} />
          <Route
            path="/project/:projectId/sites/:siteId"
            element={<div data-testid="detail">detail</div>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SiteFormPage', () => {
  beforeEach(() => {
    signInAsMember();
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
  });

  test('refuses a name the server would refuse, before sending it', async () => {
    const user = userEvent.setup();
    renderPage();
    const name = await screen.findByTestId('site-name');
    await user.type(name, 'My_Site');
    expect(screen.getByTestId('site-name-error')).toHaveTextContent(
      /lowercase letters, digits or hyphens/,
    );
    expect(screen.getByTestId('site-create')).toBeDisabled();
    await user.clear(name);
    await user.type(name, 'studio');
    expect(screen.getByTestId('site-name-error')).toHaveTextContent(/reserved/);
  });

  test('creates the site with SPA fallback on by default and opens it', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: siteFixture() } as never);
    renderPage();
    await user.type(await screen.findByTestId('site-name'), 'shop');
    expect(screen.queryByTestId('site-name-error')).not.toBeInTheDocument();
    expect(screen.getByTestId('site-spa-fallback')).toBeChecked();
    await user.click(screen.getByTestId('site-create'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/', {
        name: 'shop',
        spaFallback: true,
      }),
    );
    expect(await screen.findByTestId('detail')).toBeInTheDocument();
  });

  test('shows the server refusal, such as a taken name', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({
      response: { status: 409, data: { error: 'that site name is already taken' } },
    });
    renderPage();
    await user.type(await screen.findByTestId('site-name'), 'shop');
    await user.click(screen.getByTestId('site-spa-fallback'));
    await user.click(screen.getByTestId('site-create'));
    expect(await screen.findByRole('alert')).toHaveTextContent('that site name is already taken');
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/', {
      name: 'shop',
      spaFallback: false,
    });
  });

  test('offers no kind while server sites are dark, and sends none', async () => {
    renderPage();
    await screen.findByTestId('site-name');
    expect(screen.queryByTestId('site-kind-server')).not.toBeInTheDocument();
  });

  test('creates a server-rendered site when the server offers them', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: siteFixture() } as never);
    renderPage({ sitesserver: true });
    await user.type(await screen.findByTestId('site-name'), 'shop');
    expect(screen.getByTestId('site-kind-static')).toBeChecked();
    await user.click(screen.getByTestId('site-kind-server'));
    // Unknown paths are the framework's to answer, so the single-page switch is gone.
    expect(screen.queryByTestId('site-spa-fallback')).not.toBeInTheDocument();
    expect(screen.getByTestId('site-kind-hint')).toHaveTextContent(/Next\.js/);
    await user.click(screen.getByTestId('site-create'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/', {
        name: 'shop',
        kind: 'server',
        spaFallback: false,
      }),
    );
  });
});
