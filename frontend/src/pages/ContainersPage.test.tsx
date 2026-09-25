import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ContainersPage } from './ContainersPage';
import { api } from '../api/client';
import type { App, Deploy } from '../api/apps';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn() },
}));

const app: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'ghcr.io/acme/web:1.4.0',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'PROVISIONING',
  version: 2,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

const deploy = (overrides: Partial<Deploy>): Deploy => ({
  id: 'dep-1',
  appId: 'app-1',
  projectId: 'proj-1',
  revision: 1,
  image: app.image,
  status: 'succeeded',
  createdBy: 'user-1',
  createdAt: '2026-09-21T10:00:00Z',
  ...overrides,
});

function renderPage({
  appHosting = true,
  apps = [app],
  deploys = [deploy({})],
}: { appHosting?: boolean; apps?: App[]; deploys?: Deploy[] } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting } } as never);
    if (url === '/projects/proj-1/apps/') return Promise.resolve({ data: apps } as never);
    if (url.startsWith('/projects/proj-1/apps/app-1/deploys'))
      return Promise.resolve({ data: deploys } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/containers']}>
        <Routes>
          <Route path="/project/:projectId/containers" element={<ContainersPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('ContainersPage', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('lists each container with its status, image and last deploy', async () => {
    renderPage();
    const row = await screen.findByTestId('container-row-app-1');
    expect(row).toHaveTextContent('web');
    expect(row).toHaveTextContent('ghcr.io/acme/web:1.4.0');
    expect(await screen.findByTestId('container-status-app-1')).toHaveTextContent('Running');
    expect(screen.getByTestId('container-last-deploy-app-1')).toHaveTextContent('Revision 1');
  });

  test('shows the public URL when the API returns one', async () => {
    renderPage({ apps: [{ ...app, url: 'https://web.proj-1.example.dev' }] });
    const link = await screen.findByRole('link', { name: 'https://web.proj-1.example.dev' });
    expect(link).toHaveAttribute('href', 'https://web.proj-1.example.dev');
  });

  test('says the container was never deployed when there is no deploy yet', async () => {
    renderPage({ deploys: [] });
    expect(await screen.findByTestId('container-status-app-1')).toHaveTextContent('Not deployed');
    expect(screen.getByTestId('container-last-deploy-app-1')).toHaveTextContent('Never deployed');
  });

  test('reports a failed last deploy as the status', async () => {
    renderPage({
      deploys: [
        deploy({ status: 'failed', failureReason: 'app rollout: web ImagePullBackOff: x' }),
      ],
    });
    expect(await screen.findByTestId('container-status-app-1')).toHaveTextContent('Failed');
  });

  test('explains what a container is when the project has none', async () => {
    renderPage({ apps: [] });
    expect(await screen.findByTestId('containers-empty')).toHaveTextContent(
      /runs your own container image/i,
    );
    expect(screen.getByRole('link', { name: /new container/i })).toHaveAttribute(
      'href',
      '/project/proj-1/containers/new',
    );
  });

  test('never calls the apps API when the server has hosting off', async () => {
    renderPage({ appHosting: false });
    expect(await screen.findByTestId('containers-unavailable')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/apps/');
  });
});
