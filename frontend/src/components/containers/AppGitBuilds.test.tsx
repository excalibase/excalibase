import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppGitBuilds } from './AppGitBuilds';
import { api } from '../../api/client';
import type { AppBuild, AppGitConnection } from '../../api/appBuilds';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const BASE = '/projects/proj-1/apps/app-1';

const settings = {
  branch: 'main',
  builder: 'auto',
  rootDir: '',
  dockerfilePath: '',
  buildArgs: {},
};

const connection: AppGitConnection = {
  appId: 'app-1',
  provider: 'github',
  repository: 'acme/api',
  repositoryId: 9001,
  settings,
  connectedBy: 'u1',
  createdAt: '2026-11-05T00:00:00Z',
  updatedAt: '2026-11-05T00:00:00Z',
};

const failed: AppBuild = {
  id: 'b1',
  appId: 'app-1',
  status: 'FAILED',
  trigger: 'push',
  commitSha: 'abcdef1234567',
  commitMessage: 'feat: health route',
  repository: 'acme/api',
  app: { ...settings, maxImageBytes: 2147483648 },
  failureKind: 'image-size',
  failureReason: 'The built image is larger than 2 GiB, the limit of your plan.',
  requestedBy: 'u1',
  createdAt: '2026-11-05T00:00:00Z',
};

interface Routes {
  features?: Record<string, boolean>;
  git?: unknown;
  gitError?: { status: number; error: string };
  builds?: AppBuild[];
  repositories?: unknown;
}

function serve(routes: Routes) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const answers: Record<string, unknown> = {
      '/config': {
        deploymentMode: 'cloud',
        appHosting: true,
        features: routes.features ?? { appgitbuilds: true },
      },
      [`${BASE}/builds`]: routes.builds ?? [],
      [`${BASE}/builds/b1`]: { ...failed, log: '== docker build ==\nstep 4 failed' },
      [`${BASE}/git/repositories`]: routes.repositories ?? [],
      '/projects/proj-1/sites/build-secrets': [{ name: 'STRIPE_PK', buildVisible: false }],
    };
    if (url === `${BASE}/git`) {
      if (routes.gitError) {
        return Promise.reject({
          isAxiosError: true,
          response: { status: routes.gitError.status, data: { error: routes.gitError.error } },
        });
      }
      return Promise.resolve({ data: routes.git } as never);
    }
    if (url in answers) return Promise.resolve({ data: answers[url] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPanel(canDevelop = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AppGitBuilds projectId="proj-1" appId="app-1" canDevelop={canDevelop} />
    </QueryClientProvider>,
  );
}

const notConnected = {
  connected: false,
  connection: null,
  builders: ['auto', 'dockerfile', 'buildpacks'],
};
const connected = { connected: true, connection, builders: ['auto', 'dockerfile', 'buildpacks'] };

beforeEach(() => vi.resetAllMocks());

describe('AppGitBuilds', () => {
  test('hidden, and silent, while the appgitbuilds flag is off', async () => {
    serve({ features: {} });
    renderPanel();
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('app-git-builds')).toBeNull();
    expect(api.get).not.toHaveBeenCalledWith(`${BASE}/git`);
  });

  test('an install that cannot build says why', async () => {
    serve({
      gitError: { status: 503, error: 'git builds of apps are not switched on for this install' },
    });
    renderPanel();
    expect(await screen.findByTestId('app-git-unavailable')).toHaveTextContent('not switched on');
  });

  test('connects a repository the sign-in shows, with the builder and Dockerfile chosen', async () => {
    serve({
      git: notConnected,
      repositories: [
        {
          installation: { id: 501, account: 'acme' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ],
    });
    vi.mocked(api.put).mockResolvedValue({ data: { connection, build: failed } } as never);
    renderPanel();
    await userEvent.selectOptions(await screen.findByTestId('app-git-repository'), '9001');
    await userEvent.selectOptions(screen.getByTestId('app-build-builder'), 'dockerfile');
    await userEvent.type(screen.getByTestId('app-build-dockerfile'), 'Dockerfile.prod');
    await userEvent.click(screen.getByTestId('app-git-connect-submit'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(`${BASE}/git`, {
        repositoryId: 9001,
        settings: {
          ...settings,
          branch: '',
          builder: 'dockerfile',
          dockerfilePath: 'Dockerfile.prod',
        },
      }),
    );
  });

  test('build arguments travel as NAME=value lines', async () => {
    serve({
      git: notConnected,
      repositories: [
        {
          installation: { id: 501, account: 'acme' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ],
    });
    vi.mocked(api.put).mockResolvedValue({ data: { connection } } as never);
    renderPanel();
    await userEvent.selectOptions(await screen.findByTestId('app-git-repository'), '9001');
    await userEvent.type(screen.getByTestId('app-build-args'), 'FLAVOUR=blue');
    await userEvent.tab();
    await userEvent.click(screen.getByTestId('app-git-connect-submit'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(`${BASE}/git`, {
        repositoryId: 9001,
        settings: { ...settings, branch: '', buildArgs: { FLAVOUR: 'blue' } },
      }),
    );
  });

  test('without repositories it offers the sign-in and the App install', async () => {
    serve({ git: notConnected });
    vi.mocked(api.post).mockResolvedValue({
      data: { url: 'https://github.com/apps/x/installations/new' },
    } as never);
    const assign = vi.fn();
    Object.defineProperty(window, 'location', { value: { assign }, writable: true });
    renderPanel();
    await userEvent.click(await screen.findByTestId('app-git-install'));
    expect(api.post).toHaveBeenCalledWith(`${BASE}/git/sign-in`, { install: true });
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://github.com/apps/x/installations/new'),
    );
  });

  test('shows the builds with why one failed and its log, and rebuilds', async () => {
    serve({ git: connected, builds: [failed] });
    vi.mocked(api.post).mockResolvedValue({
      data: { ...failed, id: 'b2', status: 'QUEUED' },
    } as never);
    renderPanel();
    expect(await screen.findByTestId('app-git-connected')).toHaveTextContent('acme/api · main');
    expect(await screen.findByTestId('app-build-status-b1')).toHaveTextContent('Failed');
    expect(screen.getByTestId('app-build-reason-b1')).toHaveTextContent('larger than 2 GiB');
    await userEvent.click(screen.getByTestId('app-build-toggle-b1'));
    expect(await screen.findByTestId('app-build-log-b1')).toHaveTextContent('step 4 failed');
    await userEvent.click(screen.getByTestId('app-git-rebuild'));
    expect(api.post).toHaveBeenCalledWith(`${BASE}/builds`);
  });

  test('a queued build reads as queued, and a deployed one as deployed', async () => {
    serve({
      git: connected,
      builds: [
        { ...failed, id: 'q1', status: 'QUEUED', failureReason: undefined, failureKind: undefined },
        {
          ...failed,
          id: 'd1',
          status: 'SUCCEEDED',
          failureReason: undefined,
          failureKind: undefined,
        },
      ],
    });
    renderPanel();
    expect(await screen.findByTestId('app-build-status-q1')).toHaveTextContent('Queued');
    expect(screen.getByTestId('app-build-status-d1')).toHaveTextContent('Deployed');
  });

  test('disconnect removes the connection', async () => {
    serve({ git: connected });
    vi.mocked(api.delete).mockResolvedValue({ status: 204 } as never);
    renderPanel();
    await userEvent.click(await screen.findByTestId('app-git-disconnect'));
    // Disconnecting cancels running builds, so the page asks first, in the page.
    expect(api.delete).not.toHaveBeenCalled();
    expect(screen.getByTestId('app-git-disconnect-warning')).toHaveTextContent('running builds');
    await userEvent.click(screen.getByTestId('app-git-disconnect-confirm'));
    expect(api.delete).toHaveBeenCalledWith(`${BASE}/git`);
  });

  test('keeping the connection after the question deletes nothing', async () => {
    serve({ git: connected });
    renderPanel();
    await userEvent.click(await screen.findByTestId('app-git-disconnect'));
    await userEvent.click(screen.getByTestId('app-git-disconnect-cancel'));
    expect(api.delete).not.toHaveBeenCalled();
    expect(screen.queryByTestId('app-git-disconnect-warning')).toBeNull();
  });

  test('says when the connector lost the developer rung, and offers a reconnect', async () => {
    serve({
      git: {
        ...connected,
        connection: {
          ...connection,
          inactiveReason:
            'the member who connected this repository can no longer deploy this project',
        },
      },
    });
    renderPanel();
    expect(await screen.findByTestId('app-git-inactive')).toHaveTextContent('can no longer deploy');
    expect(screen.queryByTestId('app-git-rebuild')).toBeNull();
    expect(await screen.findByTestId('app-git-connect')).toBeInTheDocument();
    // The server lets a developer remove a stopped connection, so the page does too.
    expect(screen.getByTestId('app-git-disconnect')).toBeInTheDocument();
  });

  test('a viewer reads builds but cannot change anything', async () => {
    serve({ git: connected, builds: [failed] });
    renderPanel(false);
    expect(await screen.findByTestId('app-build-status-b1')).toBeInTheDocument();
    expect(screen.queryByTestId('app-git-rebuild')).toBeNull();
    expect(screen.queryByTestId('app-git-disconnect')).toBeNull();
    expect(screen.queryByTestId('app-build-toggle-b1')).toBeNull();
    expect(screen.queryByText('Project secrets in builds')).toBeNull();
  });

  test('a viewer of an unconnected app sees that nothing is connected', async () => {
    serve({ git: notConnected });
    renderPanel(false);
    expect(await screen.findByText('No repository is connected.')).toBeInTheDocument();
    expect(screen.queryByTestId('app-git-connect')).toBeNull();
  });

  test('says no build started even after the connection replaces the form', async () => {
    serve({
      git: notConnected,
      repositories: [
        {
          installation: { id: 501, account: 'acme' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ],
    });
    vi.mocked(api.put).mockImplementation(async () => {
      serve({ git: connected });
      return {
        data: {
          connection,
          buildError: 'this project has queued as many app builds this hour; try again in 59m',
        },
      } as never;
    });
    renderPanel();
    await userEvent.selectOptions(await screen.findByTestId('app-git-repository'), '9001');
    await userEvent.click(screen.getByTestId('app-git-connect-submit'));
    expect(await screen.findByTestId('app-git-connected')).toBeInTheDocument();
    expect(screen.getByTestId('app-git-build-error')).toHaveTextContent('no build was started');
    expect(screen.getByTestId('app-git-build-error')).toHaveTextContent('59m');
  });

  test('the notice is gone once a rebuild is asked for', async () => {
    serve({
      git: notConnected,
      repositories: [
        {
          installation: { id: 1, account: 'a' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ],
    });
    vi.mocked(api.put).mockImplementation(async () => {
      serve({ git: connected });
      return { data: { connection, buildError: 'limit used up' } } as never;
    });
    vi.mocked(api.post).mockResolvedValue({
      data: { ...failed, id: 'b2', status: 'QUEUED' },
    } as never);
    renderPanel();
    await userEvent.selectOptions(await screen.findByTestId('app-git-repository'), '9001');
    await userEvent.click(screen.getByTestId('app-git-connect-submit'));
    await userEvent.click(await screen.findByTestId('app-git-rebuild'));
    await waitFor(() => expect(screen.queryByTestId('app-git-build-error')).toBeNull());
  });

  test('a failing repository list shows its error, not the sign-in hint', async () => {
    serve({ git: notConnected });
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === `${BASE}/git/repositories`) {
        return Promise.reject({
          isAxiosError: true,
          response: { status: 500, data: { error: 'GitHub answered 502' } },
        });
      }
      if (url === `${BASE}/git`) return Promise.resolve({ data: notConnected } as never);
      if (url === '/config') {
        return Promise.resolve({
          data: { deploymentMode: 'cloud', appHosting: true, features: { appgitbuilds: true } },
        } as never);
      }
      return Promise.resolve({ data: [] } as never);
    });
    renderPanel();
    expect(await screen.findByTestId('app-git-repositories-error')).toHaveTextContent(
      'GitHub answered 502',
    );
    expect(screen.queryByText(/Sign in with GitHub so Studio sees/)).toBeNull();
  });

  test('a build argument line without = is an error, and nothing is saved', async () => {
    serve({
      git: notConnected,
      repositories: [
        {
          installation: { id: 1, account: 'a' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ],
    });
    renderPanel();
    await userEvent.selectOptions(await screen.findByTestId('app-git-repository'), '9001');
    await userEvent.type(screen.getByTestId('app-build-args'), 'FLAVOUR');
    await userEvent.tab();
    expect(screen.getByTestId('app-build-args-error')).toHaveTextContent('Line 1');
    expect(screen.getByTestId('app-git-connect-submit')).toBeDisabled();
  });

  test('a multi-line value set through the API is named, and blocks a save that would lose it', async () => {
    serve({
      git: {
        ...connected,
        connection: {
          ...connection,
          settings: { ...settings, buildArgs: { CERT: 'a\nb', FLAVOUR: 'blue' } },
        },
      },
      repositories: [
        {
          installation: { id: 1, account: 'a' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ],
    });
    renderPanel();
    await userEvent.click(await screen.findByTestId('app-git-edit'));
    expect(await screen.findByTestId('app-build-args-error')).toHaveTextContent('CERT');
    expect(screen.getByTestId('app-git-connect-submit')).toBeDisabled();
  });

  test('a build started from Studio reads as manual, a push as a push', async () => {
    serve({
      git: connected,
      builds: [failed, { ...failed, id: 'm1', trigger: 'rebuild', failureReason: undefined }],
    });
    renderPanel();
    expect(await screen.findByTestId('app-build-m1')).toHaveTextContent('Manual build of main');
    expect(screen.getByTestId('app-build-b1')).toHaveTextContent('Push to main');
  });
});
