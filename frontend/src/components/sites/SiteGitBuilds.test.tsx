import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SiteGitBuilds } from './SiteGitBuilds';
import { textToEnv, envToText } from './SiteBuildSettingsForm';
import { api } from '../../api/client';
import type { SiteBuild, SiteGitConnection } from '../../api/siteBuilds';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const BASE = '/projects/proj-1/sites/site-1';

const settings = {
  branch: 'main',
  rootDir: '',
  installCommand: '',
  buildCommand: '',
  outputDir: 'dist',
  nodeVersion: '22',
  env: {},
};

const connection: SiteGitConnection = {
  siteId: 'site-1',
  provider: 'github',
  repository: 'acme/site',
  repositoryId: 9001,
  settings,
  connectedBy: 'u1',
  createdAt: '2026-10-17T00:00:00Z',
  updatedAt: '2026-10-17T00:00:00Z',
};

const failed: SiteBuild = {
  id: 'b1',
  siteId: 'site-1',
  status: 'FAILED',
  trigger: 'push',
  commitSha: 'abcdef1234567',
  commitMessage: 'feat: hero',
  repository: 'acme/site',
  settings,
  limits: { cpuMillis: 2000, memory: '2Gi', disk: '5Gi', timeoutSeconds: 600 },
  failureKind: 'out-of-memory',
  failureReason: 'The build ran out of memory (2Gi of memory on your plan).',
  requestedBy: 'u1',
  createdAt: '2026-10-17T00:00:00Z',
};

interface Routes {
  config?: Record<string, unknown>;
  git?: unknown;
  gitError?: { status: number; error: string };
  builds?: SiteBuild[];
  repositories?: unknown;
}

function serve(routes: Routes) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const answers: Record<string, unknown> = {
      '/config': routes.config ?? { deploymentMode: 'cloud', sites: true, siteGitBuilds: true },
      [`${BASE}/builds`]: routes.builds ?? [],
      [`${BASE}/builds/b1`]: { ...failed, log: '== build ==\nKilled' },
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
      <SiteGitBuilds projectId="proj-1" siteId="site-1" canDevelop={canDevelop} />
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.resetAllMocks());

describe('SiteGitBuilds', () => {
  test('hidden while the server has git builds off', async () => {
    serve({ config: { deploymentMode: 'cloud', sites: true } });
    renderPanel();
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('site-git-builds')).toBeNull();
    expect(api.get).not.toHaveBeenCalledWith(`${BASE}/git`);
  });

  test('single-host says why it offers no git builds', async () => {
    serve({
      gitError: { status: 503, error: 'git builds are not offered on single-host installs yet' },
    });
    renderPanel();
    expect(await screen.findByTestId('git-unavailable')).toHaveTextContent('single-host');
  });

  test('connects a repository the sign-in shows and starts a build', async () => {
    serve({
      git: { connected: false, connection: null, nodeVersions: ['20', '22', '24'] },
      repositories: [
        {
          installation: { id: 501, account: 'acme' },
          repositories: [{ id: 9001, fullName: 'acme/site', defaultBranch: 'main', private: true }],
        },
      ],
    });
    vi.mocked(api.put).mockResolvedValue({ data: { connection, build: failed } } as never);
    renderPanel();
    await userEvent.selectOptions(await screen.findByTestId('git-repository'), '9001');
    await userEvent.clear(screen.getByTestId('build-root'));
    await userEvent.type(screen.getByTestId('build-root'), 'apps/web');
    await userEvent.click(screen.getByTestId('git-connect-submit'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(`${BASE}/git`, {
        repositoryId: 9001,
        settings: { ...settings, branch: '', rootDir: 'apps/web' },
      }),
    );
  });

  test('without repositories it offers the sign-in and the App install', async () => {
    serve({ git: { connected: false, connection: null, nodeVersions: ['22'] } });
    vi.mocked(api.post).mockResolvedValue({
      data: { url: 'https://github.com/apps/x/installations/new' },
    } as never);
    const assign = vi.fn();
    Object.defineProperty(window, 'location', { value: { assign }, writable: true });
    renderPanel();
    await userEvent.click(await screen.findByTestId('git-install'));
    expect(api.post).toHaveBeenCalledWith(`${BASE}/git/sign-in`, { install: true });
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://github.com/apps/x/installations/new'),
    );
  });

  test('shows the builds with why one failed, its log, and rebuilds', async () => {
    serve({ git: { connected: true, connection, nodeVersions: ['22'] }, builds: [failed] });
    vi.mocked(api.post).mockResolvedValue({
      data: { ...failed, id: 'b2', status: 'QUEUED' },
    } as never);
    renderPanel();
    expect(await screen.findByTestId('git-connected')).toHaveTextContent('acme/site · main');
    expect(await screen.findByTestId('site-build-status-b1')).toHaveTextContent('Failed');
    expect(screen.getByTestId('site-build-reason-b1')).toHaveTextContent('ran out of memory');
    await userEvent.click(screen.getByTestId('site-build-toggle-b1'));
    expect(await screen.findByTestId('site-build-log-b1')).toHaveTextContent('Killed');
    await userEvent.click(screen.getByTestId('git-rebuild'));
    expect(api.post).toHaveBeenCalledWith(`${BASE}/builds`);
  });

  test('names who connected the repository, and says when their connection stopped', async () => {
    serve({
      git: {
        connected: true,
        connection: {
          ...connection,
          connectedByName: 'alice',
          inactiveReason:
            'the member who connected this repository can no longer deploy this project',
        },
        nodeVersions: ['22'],
      },
      builds: [],
    });
    renderPanel();
    expect(await screen.findByTestId('git-connected-by')).toHaveTextContent('Connected by alice');
    expect(screen.getByTestId('git-inactive')).toHaveTextContent('can no longer deploy');
    expect(screen.queryByTestId('git-rebuild')).toBeNull();
  });

  test('a viewer reads builds but cannot change anything', async () => {
    serve({ git: { connected: true, connection, nodeVersions: ['22'] }, builds: [failed] });
    renderPanel(false);
    expect(await screen.findByTestId('site-build-status-b1')).toBeInTheDocument();
    expect(screen.queryByTestId('git-rebuild')).toBeNull();
    expect(screen.queryByTestId('git-disconnect')).toBeNull();
    expect(screen.queryByTestId('site-build-toggle-b1')).toBeNull();
    expect(screen.queryByText('Project secrets in builds')).toBeNull();
  });

  test('marking a secret build-visible warns it becomes public', async () => {
    serve({ git: { connected: true, connection, nodeVersions: ['22'] }, builds: [] });
    vi.mocked(api.put).mockResolvedValue({
      data: [{ name: 'STRIPE_PK', buildVisible: true }],
    } as never);
    renderPanel();
    await userEvent.click(await screen.findByText('Project secrets in builds'));
    expect(await screen.findByTestId('site-build-secrets-warning')).toHaveTextContent(
      'anyone can download',
    );
    await userEvent.click(screen.getByTestId('build-secret-STRIPE_PK'));
    expect(api.put).toHaveBeenCalledWith('/projects/proj-1/sites/build-secrets', {
      buildVisible: ['STRIPE_PK'],
    });
  });
});

describe('build variables text', () => {
  test('reads NAME=value lines and skips comments and blanks', () => {
    expect(textToEnv('# api\nVITE_API=https://a=b\n\n BAD \nX=')).toEqual({
      VITE_API: 'https://a=b',
      X: '',
    });
    expect(envToText({ B: '2', A: '1' })).toBe('A=1\nB=2');
  });
});
