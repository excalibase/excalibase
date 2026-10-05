import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ContainerPipelinePage } from './ContainerPipelinePage';
import { api } from '../api/client';
import type { App, Deploy } from '../api/apps';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const DIGEST_OLD = `sha256:${'11'.repeat(32)}`;
const DIGEST_NEW = `sha256:${'22'.repeat(32)}`;
const COMMIT = '9fceb02d0ae598e95dc970b74767f19372d61af8';

const app: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'ghcr.io/acme/web:main',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'ACTIVE',
  version: 4,
  createdAt: '2026-10-01T10:00:00Z',
  updatedAt: '2026-10-01T10:00:00Z',
};

const deploys: Deploy[] = [
  {
    id: 'dep-3', appId: 'app-1', projectId: 'proj-1', revision: 3, status: 'failed',
    image: `ghcr.io/acme/web@${DIGEST_NEW}`, imageRef: 'ghcr.io/acme/web:main', digest: DIGEST_NEW,
    source: 'image-watcher', failureReason: 'the container never became ready',
    createdBy: 'image-watcher', createdAt: '2026-10-06T10:00:00Z', finishedAt: '2026-10-06T10:01:05Z',
  },
  {
    id: 'dep-2', appId: 'app-1', projectId: 'proj-1', revision: 2, status: 'succeeded',
    image: `ghcr.io/acme/web@${DIGEST_OLD}`, imageRef: 'ghcr.io/acme/web:main', digest: DIGEST_OLD,
    source: 'api', commitSha: COMMIT,
    createdBy: 'user-1', createdAt: '2026-10-05T10:00:00Z', finishedAt: '2026-10-05T10:00:42Z',
  },
  {
    id: 'dep-1', appId: 'app-1', projectId: 'proj-1', revision: 1, status: 'superseded',
    image: 'ghcr.io/acme/web:main', source: 'studio',
    createdBy: 'user-1', createdAt: '2026-10-04T10:00:00Z',
  },
];

function renderPage(overrides: Partial<App> = {}) {
  const state = { app: { ...app, ...overrides } as App };
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
    if (url === '/projects/proj-1/apps/app-1') return Promise.resolve({ data: state.app } as never);
    if (url.startsWith('/projects/proj-1/apps/app-1/deploys'))
      return Promise.resolve({ data: deploys } as never);
    if (url === '/projects/proj-1/apps/app-1/logs')
      return Promise.resolve({ data: { lines: [] } } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.patch).mockImplementation((_url: string, body?: unknown) => {
    state.app = { ...state.app, ...(body as Partial<App>), version: state.app.version + 1 };
    return Promise.resolve({ data: state.app } as never);
  });
  vi.mocked(api.post).mockImplementation((url: string) =>
    Promise.resolve({
      data: { ...deploys[1], id: 'dep-4', revision: 4, status: 'pending', redeployOf: url.split('/')[6] },
    } as never),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/containers/app-1/pipeline']}>
        <Routes>
          <Route
            path="/project/:projectId/containers/:appId/pipeline"
            element={<ContainerPipelinePage pollIntervalMs={10} />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { state, user: userEvent.setup() };
}

describe('ContainerPipelinePage', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.patch).mockReset();
  });

  test('lists each deploy with where it came from, what it ran, how it ended and how long it took', async () => {
    renderPage();
    const ci = await screen.findByTestId('pipeline-deploy-dep-2');
    expect(within(ci).getByTestId('deploy-source')).toHaveTextContent('CI');
    expect(within(ci).getByTestId('deploy-source')).toHaveTextContent('9fceb02');
    expect(within(ci).getByTestId('deploy-image')).toHaveTextContent('ghcr.io/acme/web:main');
    expect(within(ci).getByTestId('deploy-image')).toHaveTextContent('111111111111');
    expect(within(ci).getByTestId('deploy-duration')).toHaveTextContent('42s');
    expect(within(ci).getByText('Live')).toBeInTheDocument();

    const watcher = screen.getByTestId('pipeline-deploy-dep-3');
    expect(within(watcher).getByTestId('deploy-source')).toHaveTextContent('Image watcher');
    expect(within(watcher).getByTestId('deploy-duration')).toHaveTextContent('1m 05s');
    expect(within(watcher).getByText(/never became ready/)).toBeInTheDocument();

    const studio = screen.getByTestId('pipeline-deploy-dep-1');
    expect(within(studio).getByTestId('deploy-source')).toHaveTextContent('Studio');
  });

  test('rolls back to an older digest after saying it leaves the database alone', async () => {
    const { user } = renderPage();
    await user.click(await screen.findByTestId('rollback-dep-2'));
    expect(screen.getByTestId('rollback-confirm-text')).toHaveTextContent(DIGEST_OLD.slice(7, 19));
    expect(screen.getByTestId('rollback-confirm-text')).toHaveTextContent(/database migrations are not touched/i);
    expect(api.post).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('rollback-confirm-dep-2'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/deploys/dep-2/redeploy'),
    );
  });

  test('the newest deploy is redeployed, not rolled back', async () => {
    const { user } = renderPage();
    expect(await screen.findByTestId('redeploy-dep-3')).toBeInTheDocument();
    expect(screen.queryByTestId('rollback-dep-3')).not.toBeInTheDocument();
    await user.click(screen.getByTestId('redeploy-dep-3'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/deploys/dep-3/redeploy'),
    );
  });

  test('auto-deploy is switched on with the version the page read', async () => {
    const { user } = renderPage();
    const toggle = await screen.findByTestId('auto-deploy-toggle');
    expect(toggle).not.toBeChecked();
    await user.click(toggle);
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(
        '/projects/proj-1/apps/app-1',
        { autoDeploy: true },
        { headers: { 'If-Match': '4' } },
      ),
    );
    await waitFor(() => expect(screen.getByTestId('auto-deploy-toggle')).toBeChecked());
  });

  test('a deploy that lands while the page is open moves the version the toggle sends', async () => {
    const rolling: Deploy = { ...deploys[0], id: 'dep-4', revision: 4, status: 'rolling', source: 'api' };
    let polls = 0;
    const { state, user } = renderPage();
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === '/config')
        return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
      if (url === '/projects/proj-1/apps/app-1') return Promise.resolve({ data: state.app } as never);
      if (url.startsWith('/projects/proj-1/apps/app-1/deploys')) {
        polls += 1;
        if (polls > 1) state.app = { ...state.app, version: 7 };
        const newest = polls > 1 ? { ...rolling, status: 'succeeded' as const } : rolling;
        return Promise.resolve({ data: [newest, ...deploys] } as never);
      }
      return Promise.resolve({ data: { lines: [] } } as never);
    });
    await waitFor(() => expect(polls).toBeGreaterThan(1));
    await user.click(await screen.findByTestId('auto-deploy-toggle'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(
        '/projects/proj-1/apps/app-1',
        { autoDeploy: true },
        { headers: { 'If-Match': '7' } },
      ),
    );
  });

  test('auto-deploy shows what the watcher last saw, and why a check failed', async () => {
    renderPage({
      autoDeploy: true,
      imageWatch: { digest: DIGEST_NEW, checkedAt: '2026-10-06T10:05:00Z', error: 'the registry is rate limiting requests' },
    });
    expect(await screen.findByTestId('auto-deploy-toggle')).toBeChecked();
    expect(screen.getByTestId('image-watch')).toHaveTextContent('ghcr.io/acme/web:main');
    expect(screen.getByTestId('image-watch')).toHaveTextContent('222222222222');
    expect(screen.getByTestId('image-watch-error')).toHaveTextContent('rate limiting');
  });

  test('an image pinned by digest has no tag to watch', async () => {
    renderPage({ image: `ghcr.io/acme/web@${DIGEST_OLD}` });
    expect(await screen.findByTestId('auto-deploy-toggle')).toBeDisabled();
    expect(screen.getByTestId('auto-deploy-pinned')).toHaveTextContent(/pinned by digest/i);
  });

  test('the GitHub Actions setup is filled in for this app, with the token as a secret', async () => {
    const { user } = renderPage();
    const snippet = await screen.findByTestId('ci-snippet');
    expect(snippet).toHaveTextContent('docker/build-push-action');
    expect(snippet).toHaveTextContent('/projects/proj-1/apps/app-1');
    expect(snippet).toHaveTextContent('secrets.EXCALIBASE_TOKEN');
    expect(snippet).toHaveTextContent('github.sha');
    expect(screen.getByTestId('ci-token-link')).toHaveAttribute('href', '/account/tokens');

    await user.click(screen.getByTestId('ci-copy'));
    expect(await navigator.clipboard.readText()).toContain('docker/login-action');
    expect(await screen.findByTestId('ci-copy')).toHaveTextContent(/copied/i);
  });
});
