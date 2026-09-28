import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider, focusManager } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ProjectOverviewPage } from './ProjectOverviewPage';
import { api } from '../api/client';
import type { App, Deploy } from '../api/apps';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn() },
}));

const instance = (overrides: Record<string, unknown> = {}) => ({
  projectId: 'proj-1',
  projectName: 'Shop',
  orgId: 'org-1',
  databaseType: 'POSTGRESQL',
  tier: 'FREE',
  postgresVersion: '17',
  status: 'ACTIVE',
  currentStage: 'COMPLETED',
  namespace: 'proj-1',
  ...overrides,
});

const app = (overrides: Partial<App> = {}): App => ({
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'ghcr.io/acme/web:1.4.0',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'FREE',
  status: 'ACTIVE',
  version: 1,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
  ...overrides,
});

const succeeded: Deploy = {
  id: 'dep-1',
  appId: 'app-1',
  projectId: 'proj-1',
  revision: 1,
  image: 'ghcr.io/acme/web:1.4.0',
  status: 'succeeded',
  createdBy: 'user-1',
  createdAt: '2026-09-21T10:00:00Z',
};

interface Stub {
  appHosting?: boolean;
  project?: Record<string, unknown> | Error;
  apps?: App[] | Error;
  endpoint?: Record<string, unknown> | Error;
  retry?: number;
}

function renderPage({
  appHosting = true,
  project = instance(),
  apps = [app()],
  endpoint,
  retry = 0,
}: Stub = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const answer = (value: unknown) =>
      value instanceof Error ? Promise.reject(value) : Promise.resolve({ data: value } as never);
    if (url === '/config') return answer({ deploymentMode: 'cloud', appHosting });
    if (url === '/provision/proj-1') return answer(project);
    if (url === '/projects/proj-1/apps/') return answer(apps);
    if (url.startsWith('/projects/proj-1/apps/app-1/deploys')) return answer([succeeded]);
    if (url === '/projects/proj-1/db-endpoint')
      return answer(
        endpoint ?? { publicEnabled: false, available: false, host: '', port: 0, internal: {} },
      );
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry, retryDelay: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1']}>
        <Routes>
          <Route path="/project/:projectId" element={<ProjectOverviewPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('ProjectOverviewPage', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('shows the database and the containers as two services, each with its own status', async () => {
    renderPage();
    const database = await screen.findByTestId('service-database');
    const containers = await screen.findByTestId('service-containers');
    expect(await within(database).findByTestId('service-database-status')).toHaveTextContent(
      'Running',
    );
    expect(within(database).getByRole('link', { name: /open database/i })).toHaveAttribute(
      'href',
      '/project/proj-1/database/overview',
    );
    expect(within(containers).getByTestId('service-containers-summary')).toHaveTextContent(
      '1 container',
    );
    expect(await within(containers).findByTestId('service-app-status-app-1')).toHaveTextContent(
      'Running',
    );
    expect(within(containers).getByRole('link', { name: /open containers/i })).toHaveAttribute(
      'href',
      '/project/proj-1/containers',
    );
  });

  test('hides the containers service when the server has app hosting off', async () => {
    renderPage({ appHosting: false });
    expect(await screen.findByTestId('service-database')).toBeInTheDocument();
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('service-containers')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/apps/');
  });

  test('a failed database does not make the containers look broken', async () => {
    renderPage({ project: instance({ status: 'FAILED', currentStage: 'FAILED' }) });
    const database = await screen.findByTestId('service-database');
    expect(await within(database).findByTestId('service-database-status')).toHaveTextContent(
      'Failed',
    );
    const containers = screen.getByTestId('service-containers');
    expect(await within(containers).findByTestId('service-app-status-app-1')).toHaveTextContent(
      'Running',
    );
    expect(within(containers).queryByRole('alert')).toBeNull();
  });

  test('containers that cannot be loaded do not make the database look broken', async () => {
    renderPage({ apps: new Error('network down') });
    const containers = await screen.findByTestId('service-containers');
    expect(await within(containers).findByRole('alert')).toHaveTextContent(
      /could not load the containers/i,
    );
    const database = screen.getByTestId('service-database');
    expect(await within(database).findByTestId('service-database-status')).toHaveTextContent(
      'Running',
    );
    expect(within(database).queryByRole('alert')).toBeNull();
  });

  test('a database that cannot be loaded says so inside its own card only', async () => {
    renderPage({ project: new Error('boom') });
    const database = await screen.findByTestId('service-database');
    expect(await within(database).findByRole('alert')).toHaveTextContent(
      /could not load the database/i,
    );
    const containers = screen.getByTestId('service-containers');
    expect(await within(containers).findByTestId('service-app-status-app-1')).toHaveTextContent(
      'Running',
    );
  });

  test('a project with no containers explains what a container is and what it costs', async () => {
    renderPage({ apps: [] });
    const empty = await screen.findByTestId('service-containers-empty');
    expect(empty).toHaveTextContent(/your own container image/i);
    expect(empty).toHaveTextContent(/plan includes/i);
    expect(within(empty).getByRole('link', { name: /new container/i })).toHaveAttribute(
      'href',
      '/project/proj-1/containers/new',
    );
  });

  test('a container that reads the database shows that link on the overview', async () => {
    renderPage({
      apps: [
        app({
          env: [
            {
              name: 'DATABASE_URL',
              kind: 'reference',
              reference: { sourceKind: 'database', sourceName: 'proj-1', variable: 'DATABASE_URL' },
            },
          ],
        }),
      ],
    });
    expect(await screen.findByTestId('service-app-uses-database-app-1')).toHaveTextContent(
      /uses the database/i,
    );
  });

  test('a container with no database variables shows no link', async () => {
    renderPage();
    expect(await screen.findByTestId('service-app-status-app-1')).toBeInTheDocument();
    expect(screen.queryByTestId('service-app-uses-database-app-1')).toBeNull();
  });

  test('says the database is private by default and the containers are public by default', async () => {
    renderPage();
    const database = await screen.findByTestId('service-database');
    expect(await within(database).findByTestId('service-database-access')).toHaveTextContent(
      /private by default/i,
    );
    expect(
      within(screen.getByTestId('service-containers')).getByTestId('service-containers-access'),
    ).toHaveTextContent(/public by default/i);
  });

  test('shows the public database port when an admin has opened one', async () => {
    renderPage({
      endpoint: {
        publicEnabled: true,
        available: true,
        host: 'proj-1.db.example.com',
        port: 30001,
        internal: {},
      },
    });
    const access = await screen.findByTestId('service-database-access');
    await waitFor(() => expect(access).toHaveTextContent('proj-1.db.example.com:30001'));
  });
});

describe('ProjectOverviewPage while a retry is paused', () => {
  afterEach(() => focusManager.setFocused(undefined));

  test('a paused retry is not mistaken for a project with no containers', async () => {
    focusManager.setFocused(false);
    renderPage({ apps: new Error('network down'), retry: 1 });
    expect(await screen.findByTestId('service-containers')).toBeInTheDocument();
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/projects/proj-1/apps/'));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByTestId('service-containers-empty')).toBeNull();
  });
});
