import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SitesPage } from './SitesPage';
import { api } from '../api/client';
import type { Site } from '../api/sites';
import { siteFixture } from '../test/siteFixtures';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function renderPage({ sites = true, list = [siteFixture()] as Site[] | Error } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({
        data: { deploymentMode: 'cloud', appHosting: true, sites },
      } as never);
    if (url === '/projects/proj-1/sites/') {
      return list instanceof Error
        ? Promise.reject(list)
        : Promise.resolve({ data: list } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/sites']}>
        <Routes>
          <Route path="/project/:projectId/sites" element={<SitesPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SitesPage', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('lists each site with its live URL and a link to it', async () => {
    renderPage();
    const row = await screen.findByTestId('site-row-site-1');
    expect(row).toHaveTextContent('shop');
    expect(screen.getByRole('link', { name: 'shop' })).toHaveAttribute(
      'href',
      '/project/proj-1/sites/site-1',
    );
    expect(screen.getByRole('link', { name: 'https://shop.example.app' })).toHaveAttribute(
      'href',
      'https://shop.example.app',
    );
    expect(screen.getByTestId('sites-new')).toHaveAttribute('href', '/project/proj-1/sites/new');
  });

  test('marks a site that has never been deployed', async () => {
    renderPage({ list: [siteFixture({ liveDeployId: undefined })] });
    expect(await screen.findByTestId('site-status-site-1')).toHaveTextContent('Not deployed');
  });

  test('explains Sites when the project has none', async () => {
    renderPage({ list: [] });
    expect(await screen.findByTestId('sites-empty')).toHaveTextContent(/build output/i);
  });

  test('shows the server refusal when the list cannot be read', async () => {
    renderPage({
      list: Object.assign(new Error('x'), { response: { status: 500, data: { error: 'boom' } } }),
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('boom');
  });

  test('points at containers and never calls the sites API when the install has no Sites', async () => {
    renderPage({ sites: false });
    const pointer = await screen.findByTestId('sites-unavailable');
    expect(pointer).toHaveTextContent(
      "Static frontend hosting isn't available on this install; deploy your frontend as a container.",
    );
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/sites/');
    expect(screen.queryByTestId('sites-new')).not.toBeInTheDocument();
  });
});
