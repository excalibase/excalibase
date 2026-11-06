import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockProject, mockProjectRole, mockSchemaEndpoints } from './helpers';

// Git builds of apps (EXC-547): connect a repository from the container page, shown only with appgitbuilds on.
const APP_PATH = '/api/projects/test-project/apps/app-1';

const app = {
  id: 'app-1',
  projectId: 'test-project',
  name: 'api',
  image: 'registry.local/p-test/api@sha256:aa',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'ACTIVE',
  version: 2,
  createdAt: '2026-11-01T10:00:00Z',
  updatedAt: '2026-11-01T10:00:00Z',
};

const settings = {
  branch: 'main',
  builder: 'auto',
  rootDir: '',
  dockerfilePath: '',
  buildArgs: {},
};
const connection = {
  appId: 'app-1',
  projectId: 'test-project',
  provider: 'github',
  installationId: 501,
  repositoryId: 9001,
  repository: 'acme/api',
  connectedBy: 'u1',
  settings,
  createdAt: '2026-11-05T10:00:00Z',
  updatedAt: '2026-11-05T10:00:00Z',
};
const build = (status: string, extra: Record<string, unknown> = {}) => ({
  id: 'b1',
  kind: 'app',
  appId: 'app-1',
  projectId: 'test-project',
  status,
  trigger: 'push',
  commitSha: 'abcdef1234567',
  commitMessage: 'feat: health route',
  repository: 'acme/api',
  provider: 'github',
  app: { ...settings, maxImageBytes: 2147483648 },
  requestedBy: 'u1',
  createdAt: '2026-11-05T10:00:00Z',
  ...extra,
});

interface Recorded {
  connects: unknown[];
  signIns: unknown[];
  rebuilds: number;
  disconnects: number;
}

const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function mockAppGit(
  page: Page,
  opts: {
    flag: boolean;
    connected?: boolean;
    builds?: unknown[];
    buildError?: string;
    // Slow answers for the config and the app, as a loaded server gives.
    delayMs?: number;
  },
): Promise<Recorded> {
  const recorded: Recorded = { connects: [], signIns: [], rebuilds: 0, disconnects: 0 };
  let connected = opts.connected ?? false;
  let builds = opts.builds ?? [];
  const slowly = async () => {
    if (opts.delayMs) await new Promise((resolve) => setTimeout(resolve, opts.delayMs));
  };
  await page.route('**/api/config', async (route) => {
    await slowly();
    return json(route, {
      deploymentMode: 'cloud',
      appHosting: true,
      features: { appgitbuilds: opts.flag },
    });
  });
  await page.route(new RegExp(`${APP_PATH}$`), async (route) => {
    await slowly();
    return json(route, app);
  });
  await page.route(new RegExp(`${APP_PATH}/deploys(\\?.*)?$`), (route) => json(route, []));
  await page.route(new RegExp(`${APP_PATH}/logs`), (route) => json(route, { lines: [] }));
  await page.route('**/api/projects/test-project/sites/build-secrets', (route) => json(route, []));
  await page.route(new RegExp(`${APP_PATH}/git(/.*)?$`), async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(`${APP_PATH}/git`, '');
    const method = request.method();
    if (path === '' && method === 'GET') {
      return json(route, {
        connected,
        connection: connected ? connection : null,
        builders: ['auto', 'dockerfile', 'buildpacks'],
      });
    }
    if (path === '' && method === 'PUT') {
      recorded.connects.push(request.postDataJSON());
      connected = true;
      if (opts.buildError) return json(route, { connection, buildError: opts.buildError });
      // The first build of a new connection is started by Studio, not by a push.
      builds = [build('QUEUED', { trigger: 'rebuild' })];
      return json(route, { connection, build: builds[0] });
    }
    if (path === '' && method === 'DELETE') {
      recorded.disconnects++;
      connected = false;
      return route.fulfill({ status: 204 });
    }
    if (path === '/repositories') {
      return json(route, [
        {
          installation: { id: 501, account: 'acme' },
          repositories: [{ id: 9001, fullName: 'acme/api', defaultBranch: 'main', private: true }],
        },
      ]);
    }
    if (path === '/sign-in' && method === 'POST') {
      recorded.signIns.push(request.postDataJSON());
      return json(route, {
        url: 'https://github.example/login/oauth/authorize?client_id=x&state=s1',
      });
    }
    return json(route, { error: `unmocked ${method} ${path}` }, 500);
  });
  await page.route(new RegExp(`${APP_PATH}/builds(/.*)?$`), async (route) => {
    const request = route.request();
    if (request.method() === 'POST') {
      recorded.rebuilds++;
      return json(route, build('QUEUED', { id: 'b2', trigger: 'rebuild' }), 202);
    }
    if (request.url().endsWith('/builds/b1'))
      return json(route, build('FAILED', { log: '== docker build ==\nstep 4 failed' }));
    return json(route, builds);
  });
  return recorded;
}

test.describe('App git builds', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('a developer signs in, picks a repository and a builder, and sees the first build queued', async ({
    page,
  }) => {
    await mockProjectRole(page, 'developer');
    await page.route('https://github.example/**', (route) =>
      route.fulfill({ status: 200, contentType: 'text/html', body: '<h1>GitHub</h1>' }),
    );
    const recorded = await mockAppGit(page, { flag: true });
    await page.goto('/project/test-project/containers/app-1');

    await expect(page.getByTestId('app-git-builds')).toBeVisible();
    await page.getByTestId('app-git-sign-in').click();
    await expect.poll(() => recorded.signIns).toEqual([{ install: false }]);
    await expect(page).toHaveURL(/github\.example\/login\/oauth\/authorize/);
    await page.goBack();

    await page.getByTestId('app-git-repository').selectOption('9001');
    await page.getByTestId('app-build-builder').selectOption('dockerfile');
    await page.getByTestId('app-build-dockerfile').fill('Dockerfile.prod');
    await page.getByTestId('app-git-connect-submit').click();

    await expect
      .poll(() => recorded.connects)
      .toEqual([
        {
          repositoryId: 9001,
          settings: {
            ...settings,
            branch: '',
            builder: 'dockerfile',
            dockerfilePath: 'Dockerfile.prod',
          },
        },
      ]);
    await expect(page.getByTestId('app-git-connected')).toContainText('acme/api · main');
    await expect(page.getByTestId('app-build-status-b1')).toHaveText('Queued');
    await expect(page.getByTestId('app-build-b1')).toContainText('Manual build of main');
  });

  test('a connected app shows a failed build with its reason and log, rebuilds, and disconnects', async ({
    page,
  }) => {
    await mockProjectRole(page, 'developer');
    const failed = build('FAILED', {
      failureKind: 'image-size',
      failureReason: 'The built image is larger than 2 GiB, the limit of your plan.',
    });
    const recorded = await mockAppGit(page, { flag: true, connected: true, builds: [failed] });
    await page.goto('/project/test-project/containers/app-1');

    await expect(page.getByTestId('app-build-status-b1')).toHaveText('Failed');
    await expect(page.getByTestId('app-build-reason-b1')).toContainText('larger than 2 GiB');
    await page.getByTestId('app-build-toggle-b1').click();
    await expect(page.getByTestId('app-build-log-b1')).toContainText('step 4 failed');
    await page.getByTestId('app-git-rebuild').click();
    await expect.poll(() => recorded.rebuilds).toBe(1);
    await page.getByTestId('app-git-disconnect').click();
    await expect(page.getByTestId('app-git-disconnect-warning')).toContainText('running builds');
    expect(recorded.disconnects).toBe(0);
    await page.getByTestId('app-git-disconnect-confirm').click();
    await expect.poll(() => recorded.disconnects).toBe(1);
    await expect(page.getByTestId('app-git-connect')).toBeVisible();
  });

  test('a viewer sees the connection and the builds, and no control that changes anything', async ({
    page,
  }) => {
    await mockProjectRole(page, 'viewer');
    await mockAppGit(page, { flag: true, connected: true, builds: [build('SUCCEEDED')] });
    await page.goto('/project/test-project/containers/app-1');

    await expect(page.getByTestId('app-git-connected')).toContainText('acme/api');
    await expect(page.getByTestId('app-build-status-b1')).toHaveText('Deployed');
    await expect(page.getByTestId('app-git-rebuild')).toHaveCount(0);
    await expect(page.getByTestId('app-git-disconnect')).toHaveCount(0);
    await expect(page.getByTestId('app-build-toggle-b1')).toHaveCount(0);
  });

  test('with the flag off the container page has no git section and never asks for one', async ({
    page,
  }) => {
    await mockProjectRole(page, 'developer');
    await mockAppGit(page, { flag: false, delayMs: 800 });
    // Registered last, so it answers first: Playwright runs the newest matching route.
    const asked: string[] = [];
    await page.route(new RegExp(`${APP_PATH}/(git|builds)`), (route) => {
      asked.push(route.request().url());
      return json(route, {}, 404);
    });
    const config = page.waitForResponse('**/api/config');
    await page.goto('/project/test-project/containers/app-1');

    await config;
    // The section only mounts once the app has loaded, so wait for the app itself, then let a stray call leave.
    await expect(page.getByText(app.image)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('app-git-builds')).toHaveCount(0);
    await page.waitForTimeout(500);
    expect(asked).toEqual([]);
  });

  test('the hourly limit leaves a message after the form is gone', async ({ page }) => {
    await mockProjectRole(page, 'developer');
    await mockAppGit(page, {
      flag: true,
      buildError:
        'this project has queued as many app builds this hour as its plan allows; try again in 59m32s',
    });
    await page.goto('/project/test-project/containers/app-1');

    await page.getByTestId('app-git-repository').selectOption('9001');
    await page.getByTestId('app-git-connect-submit').click();
    await expect(page.getByTestId('app-git-connected')).toBeVisible();
    await expect(page.getByTestId('app-git-build-error')).toContainText('no build was started');
    await expect(page.getByTestId('app-git-build-error')).toContainText('59m32s');
  });

  test('a build argument line without = is refused in the page', async ({ page }) => {
    await mockProjectRole(page, 'developer');
    const recorded = await mockAppGit(page, { flag: true });
    await page.goto('/project/test-project/containers/app-1');

    await page.getByTestId('app-git-repository').selectOption('9001');
    await page.getByTestId('app-build-args').fill('FLAVOUR');
    await page.getByTestId('app-build-branch').click();
    await expect(page.getByTestId('app-build-args-error')).toContainText('Line 1');
    await expect(page.getByTestId('app-git-connect-submit')).toBeDisabled();
    expect(recorded.connects).toEqual([]);
  });
});
