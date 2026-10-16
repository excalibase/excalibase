import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SitesPage } from './SitesPage';
import { SiteDetailPage } from './SiteDetailPage';
import { SiteFormPage } from './SiteFormPage';
import { api } from '../api/client';
import type { Site } from '../api/sites';
import { useAuthStore } from '../stores/auth-store';
import { siteDeployFixture, siteFixture } from '../test/siteFixtures';

// A viewer reads a project's sites but may not change them (the server answers
// 403): Studio offers them no control that can only fail (EXC-353).

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const older = siteDeployFixture({ id: 'dep-1' });
const live = siteDeployFixture({ id: 'dep-2', live: true, createdAt: '2026-10-12T10:00:00Z' });

function render_(path: string, orgRole: string, list: Site[] = [siteFixture()]) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', sites: true } } as never);
    if (url === '/provision/proj-1')
      return Promise.resolve({ data: { projectId: 'proj-1', orgId: 'o1' } } as never);
    if (url === '/orgs')
      return Promise.resolve({
        data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }],
      } as never);
    if (url === '/projects/proj-1/sites/') return Promise.resolve({ data: list } as never);
    if (url === '/projects/proj-1/sites/site-1')
      return Promise.resolve({ data: siteFixture() } as never);
    if (url === '/projects/proj-1/sites/site-1/deploys')
      return Promise.resolve({ data: [live, older] } as never);
    if (url === '/projects/proj-1/sites/site-1/tokens')
      return Promise.resolve({ data: [] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/project/:projectId/sites" element={<SitesPage />} />
          <Route path="/project/:projectId/sites/new" element={<SiteFormPage />} />
          <Route path="/project/:projectId/sites/:siteId" element={<SiteDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const writeControls = [
  'site-upload',
  'site-delete',
  'site-spa-toggle',
  'site-rollback-dep-1',
  'site-deploy-tokens',
];

describe('Sites for a viewer', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    useAuthStore
      .getState()
      .setAuth({ id: 'u1', username: 'viewer', email: 'v@x.test', role: 'user' });
  });

  test('the list offers no new site', async () => {
    render_('/project/proj-1/sites', 'viewer');
    expect(await screen.findByTestId('site-row-site-1')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByTestId('sites-new')).not.toBeInTheDocument());
  });

  test('an empty list offers no new site either', async () => {
    render_('/project/proj-1/sites', 'viewer', []);
    expect(await screen.findByTestId('sites-empty')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /new site/i })).not.toBeInTheDocument();
  });

  test('a site shows its URL, routing and history but no control that changes it', async () => {
    render_('/project/proj-1/sites/site-1', 'viewer');
    expect(await screen.findByTestId('site-url')).toBeInTheDocument();
    expect(await screen.findByTestId('site-deploy-row-dep-1')).toBeInTheDocument();
    expect(screen.getByTestId('site-spa-state')).toHaveTextContent(/on/i);
    for (const id of writeControls) expect(screen.queryByTestId(id)).not.toBeInTheDocument();
    // Deploy tokens are a Developer's: a viewer's Studio never asks for them.
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/sites/site-1/tokens');
  });

  test('the new-site address says a developer creates sites, with no form', async () => {
    render_('/project/proj-1/sites/new', 'viewer');
    expect(await screen.findByTestId('site-create-not-allowed')).toBeInTheDocument();
    expect(screen.queryByTestId('site-create')).not.toBeInTheDocument();
  });

  test('a developer gets every control', async () => {
    render_('/project/proj-1/sites/site-1', 'developer');
    for (const id of writeControls) expect(await screen.findByTestId(id)).toBeInTheDocument();
  });
});
