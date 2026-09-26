import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ContainerDetailPage } from './ContainerDetailPage';
import { api } from '../api/client';
import type { App, Deploy, DeployStatus } from '../api/apps';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
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
  app?: Partial<App>;
  deploys: Deploy[];
  // Statuses the newest deploy moves through on each successive poll.
  progression?: Array<{ status: DeployStatus; failureReason?: string }>;
}

function renderPage(scenario: Scenario) {
  const state = {
    app: { ...app, ...scenario.app },
    deploys: [...scenario.deploys],
    progression: [...(scenario.progression ?? [])],
  };
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
    if (url === '/projects/proj-1/apps/app-1') return Promise.resolve({ data: state.app } as never);
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
  vi.mocked(api.delete).mockResolvedValue({ status: 204 } as never);
  vi.mocked(api.post).mockImplementation((url: string) => {
    const lifecycle = url.match(/\/apps\/app-1\/(pause|resume)$/);
    if (lifecycle) {
      state.app = { ...state.app, status: lifecycle[1] === 'pause' ? 'PAUSED' : 'ACTIVE' };
      return Promise.resolve({ data: { id: 'app-1', status: state.app.status } } as never);
    }
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
          <Route path="/project/:projectId/containers" element={<p>containers list</p>} />
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
    vi.mocked(api.delete).mockReset();
  });

  test('pause stops a running container and shows it paused', async () => {
    const { user } = renderPage({ app: { status: 'ACTIVE' }, deploys: [deploy({})] });
    await user.click(await screen.findByTestId('pause-button'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/pause'));
    await waitFor(() => expect(screen.getByTestId('container-status')).toHaveTextContent('Paused'));
    expect(screen.queryByTestId('pause-button')).not.toBeInTheDocument();
    expect(screen.getByTestId('resume-button')).toBeInTheDocument();
  });

  test('resume starts a paused container again', async () => {
    const { user } = renderPage({ app: { status: 'PAUSED' }, deploys: [deploy({})] });
    await user.click(await screen.findByTestId('resume-button'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/resume'));
    await waitFor(() => expect(screen.getByTestId('container-status')).toHaveTextContent('Running'));
  });

  test('a container that never ran offers neither pause nor resume', async () => {
    renderPage({ deploys: [] });
    expect(await screen.findByTestId('delete-button')).toBeInTheDocument();
    expect(screen.queryByTestId('pause-button')).not.toBeInTheDocument();
    expect(screen.queryByTestId('resume-button')).not.toBeInTheDocument();
  });

  test('delete asks for confirmation, then removes the container and goes back to the list', async () => {
    const { user } = renderPage({ app: { status: 'ACTIVE' }, deploys: [deploy({})] });
    await user.click(await screen.findByTestId('delete-button'));
    expect(api.delete).not.toHaveBeenCalled();
    expect(screen.getByTestId('delete-confirm-text')).toHaveTextContent(/stops the container/i);

    await user.click(screen.getByTestId('delete-cancel'));
    expect(screen.queryByTestId('delete-confirm-text')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('delete-button'));
    await user.click(screen.getByTestId('delete-confirm'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/apps/app-1'));
    expect(await screen.findByText('containers list')).toBeInTheDocument();
  });

  test('a refused lifecycle action says why', async () => {
    const { user } = renderPage({ app: { status: 'ACTIVE' }, deploys: [deploy({})] });
    vi.mocked(api.post).mockRejectedValueOnce({
      response: { data: { error: 'the app is being paused, resumed or deleted' } },
    });
    await user.click(await screen.findByTestId('pause-button'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/being paused, resumed or deleted/);
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
