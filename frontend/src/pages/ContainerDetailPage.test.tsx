import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ContainerDetailPage } from './ContainerDetailPage';
import { api } from '../api/client';
import type { App, Deploy, DeployStatus } from '../api/apps';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn() },
}));

const app: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [
    { name: 'MODE', kind: 'literal', value: 'production-literal-value' },
    {
      name: 'DATABASE_URL',
      kind: 'reference',
      reference: { sourceKind: 'database', sourceName: 'appdb', variable: 'DATABASE_URL' },
    },
    { name: 'API_KEY', kind: 'secret', secret: { path: 'projects/proj-1/stripe', key: 'api_key' } },
  ],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'PROVISIONING',
  version: 3,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

const deploy = (overrides: Partial<Deploy>): Deploy => ({
  id: 'dep-1',
  appId: 'app-1',
  projectId: 'proj-1',
  revision: 1,
  image: 'nginx:1.26',
  status: 'succeeded',
  createdBy: 'user-1',
  createdAt: '2026-09-21T10:00:00Z',
  ...overrides,
});

interface Scenario {
  deploys: Deploy[];
  // Statuses the newest deploy moves through on each successive poll.
  progression?: Array<{ status: DeployStatus; failureReason?: string }>;
}

function renderPage(scenario: Scenario) {
  const state = { deploys: [...scenario.deploys], progression: [...(scenario.progression ?? [])] };
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
    if (url === '/projects/proj-1/apps/app-1') return Promise.resolve({ data: app } as never);
    if (url.startsWith('/projects/proj-1/apps/app-1/deploys')) {
      const newest = state.deploys[0];
      const next =
        newest && ['pending', 'rolling'].includes(newest.status)
          ? state.progression.shift()
          : undefined;
      if (next) state.deploys[0] = { ...newest, ...next };
      return Promise.resolve({ data: [...state.deploys] } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.post).mockImplementation((url: string) => {
    const revision = state.deploys.length + 1;
    if (url === '/projects/proj-1/apps/app-1/deploy') {
      const created = deploy({
        id: `dep-${revision}`,
        revision,
        image: app.image,
        status: 'pending',
      });
      state.deploys = [created, ...state.deploys];
      return Promise.resolve({ data: created } as never);
    }
    const redeploy = url.match(/\/deploys\/([^/]+)\/redeploy$/);
    if (redeploy) {
      const source = state.deploys.find((d) => d.id === redeploy[1]);
      const created = deploy({
        id: `dep-${revision}`,
        revision,
        image: source?.image,
        status: 'pending',
        redeployOf: redeploy[1],
      });
      state.deploys = [created, ...state.deploys];
      return Promise.resolve({ data: created } as never);
    }
    return Promise.reject(new Error(`unexpected POST ${url}`));
  });

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/containers/app-1']}>
        <Routes>
          <Route
            path="/project/:projectId/containers/:appId"
            element={<ContainerDetailPage pollIntervalMs={10} />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { state, user: userEvent.setup() };
}

describe('ContainerDetailPage', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
  });

  test('deploy shows the rollout moving from queued to rolling out to live', async () => {
    const { user } = renderPage({
      deploys: [],
      progression: [
        { status: 'pending' },
        { status: 'rolling' },
        { status: 'rolling' },
        { status: 'succeeded' },
      ],
    });
    await user.click(await screen.findByTestId('deploy-button'));
    const status = await screen.findByTestId('current-deploy-status');
    await waitFor(() => expect(status).toHaveTextContent('Rolling out'));
    await waitFor(() => expect(status).toHaveTextContent('Live'));
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/deploy');
  });

  test('a failed deploy says why in plain words', async () => {
    const { user } = renderPage({
      deploys: [],
      progression: [
        { status: 'pending' },
        {
          status: 'failed',
          failureReason: 'app rollout: web ImagePullBackOff: Back-off pulling image "nginx:9.9"',
        },
      ],
    });
    await user.click(await screen.findByTestId('deploy-button'));
    await waitFor(() =>
      expect(screen.getByTestId('current-deploy-status')).toHaveTextContent('Failed'),
    );
    expect(screen.getByTestId('current-deploy-reason')).toHaveTextContent(
      /image could not be pulled/i,
    );
  });

  test('lists deployments newest first with revision, image and status', async () => {
    renderPage({
      deploys: [
        deploy({ id: 'dep-1', revision: 1 }),
        deploy({ id: 'dep-2', revision: 2, image: 'nginx:1.27', status: 'failed' }),
      ],
    });
    const rows = await screen.findAllByTestId(/^deploy-row-/);
    expect(rows[0]).toHaveTextContent('#2');
    expect(rows[0]).toHaveTextContent('nginx:1.27');
    expect(rows[0]).toHaveTextContent('Failed');
    expect(rows[1]).toHaveTextContent('#1');
    expect(rows[1]).toHaveTextContent('nginx:1.26');
  });

  test('redeploy asks for confirmation on the page, then rolls the older deploy out again', async () => {
    const { user } = renderPage({
      deploys: [
        deploy({ id: 'dep-2', revision: 2, image: 'nginx:1.27' }),
        deploy({ id: 'dep-1', revision: 1 }),
      ],
    });
    expect(await screen.findByTestId('deploy-row-dep-2')).toBeInTheDocument();
    expect(screen.queryByTestId('redeploy-dep-2')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('redeploy-dep-1'));
    const row = screen.getByTestId('deploy-row-dep-1');
    expect(within(row).getByText(/roll back to revision 1/i)).toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();

    await user.click(within(row).getByTestId('redeploy-cancel-dep-1'));
    expect(within(row).queryByText(/roll back to revision 1/i)).not.toBeInTheDocument();

    await user.click(screen.getByTestId('redeploy-dep-1'));
    await user.click(screen.getByTestId('redeploy-confirm-dep-1'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/deploys/dep-1/redeploy'),
    );
    expect(await screen.findByTestId('deploy-row-dep-3')).toHaveTextContent('nginx:1.26');
  });

  test('variables are masked and never show a value or a secret location', async () => {
    renderPage({ deploys: [] });
    expect(await screen.findByTestId('env-view-MODE')).toBeInTheDocument();
    expect(screen.queryByText('production-literal-value')).not.toBeInTheDocument();
    expect(screen.queryByText(/stripe/)).not.toBeInTheDocument();
    expect(screen.getByTestId('env-view-DATABASE_URL')).toHaveTextContent(/database appdb/i);
    expect(screen.getByTestId('env-view-API_KEY')).toHaveTextContent(/secret/i);
  });
});
