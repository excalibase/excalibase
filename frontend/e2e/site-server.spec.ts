import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockProject, mockProjectRole, mockSchemaEndpoints } from './helpers';

// Server-rendered sites (EXC-661): the kind choice shows only while /api/config reports sitesserver,
// a server site connects a repository with the Next.js preset, and a build the preset refuses points to a container.
const SITE_PATH = '/api/projects/test-project/sites';

const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function mockConfig(page: Page, serverSites: boolean) {
  await page.route('**/api/config', (route) =>
    json(route, {
      deploymentMode: 'cloud',
      appHosting: true,
      sites: true,
      siteGitBuilds: true,
      features: { sitesserver: serverSites },
    }),
  );
}

const settings = { branch: 'main', rootDir: '', installCommand: '', buildCommand: '', outputDir: '', nodeVersion: '22', framework: 'nextjs', env: {} };

async function mockSites(page: Page) {
  const recorded = { created: [] as unknown[], connected: [] as unknown[] };
  let connected = false;
  const site = {
    id: 'site-1', projectId: 'test-project', name: 'shop', hostname: 'shop.example.app',
    url: 'https://shop.example.app', spaFallback: false, kind: 'server',
  };
  const refused = {
    id: 'b1', siteId: 'site-1', status: 'FAILED', trigger: 'push', commitSha: 'abcdef1234567', repository: 'acme/shop',
    settings, limits: { cpuMillis: 2000, memory: '2Gi', disk: '5Gi', timeoutSeconds: 600 },
    failureKind: 'framework', failureReason: 'this Next.js app cannot run as a server-rendered site (the log says why)',
    requestedBy: 'u1', createdAt: '2026-11-06T10:00:00Z',
  };
  await page.route(new RegExp(`${SITE_PATH}(/.*)?$`), async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(SITE_PATH, '');
    const method = request.method();
    if (path === '/' && method === 'POST') {
      recorded.created.push(request.postDataJSON());
      return json(route, site, 201);
    }
    if (path === '/' && method === 'GET') return json(route, []);
    if (path === '/site-1' && method === 'GET') return json(route, site);
    if (path === '/site-1/builds') return json(route, connected ? [refused] : []);
    if (['/site-1/deploys', '/site-1/tokens', '/site-1/domains', '/build-secrets'].includes(path)) return json(route, []);
    if (path === '/site-1/git/repositories')
      return json(route, [{ installation: { id: 5, account: 'acme' }, repositories: [{ id: 1, fullName: 'acme/shop', defaultBranch: 'main', private: true }] }]);
    if (path === '/site-1/git' && method === 'PUT') {
      recorded.connected.push(request.postDataJSON());
      connected = true;
      return json(route, { connection: { siteId: 'site-1', provider: 'github', repository: 'acme/shop', repositoryId: 1, settings, connectedBy: 'u1', createdAt: '', updatedAt: '' }, build: refused });
    }
    if (path === '/site-1/git')
      return json(route, connected
        ? { connected: true, nodeVersions: ['22'], connection: { siteId: 'site-1', provider: 'github', repository: 'acme/shop', repositoryId: 1, settings, connectedBy: 'u1', createdAt: '', updatedAt: '' } }
        : { connected: false, connection: null, nodeVersions: ['22'] });
    return json(route, { error: `unmocked ${method} ${path}` }, 404);
  });
  return recorded;
}

test.describe('Server-rendered sites', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
    await mockProjectRole(page, 'developer');
  });

  test('a developer creates a server site, connects Next.js and is pointed to a container when it does not fit', async ({ page }) => {
    await mockConfig(page, true);
    const recorded = await mockSites(page);

    await page.goto('/project/test-project/sites/new');
    await page.getByTestId('site-name').fill('shop');
    await page.getByTestId('site-kind-server').check();
    await expect(page.getByTestId('site-spa-fallback')).toHaveCount(0);
    await page.getByTestId('site-create').click();
    await expect.poll(() => recorded.created).toEqual([{ name: 'shop', kind: 'server', spaFallback: false }]);

    await page.goto('/project/test-project/sites/site-1');
    await page.getByTestId('git-repository').selectOption('1');
    await expect(page.getByTestId('build-framework')).toHaveValue('nextjs');
    await expect(page.getByTestId('build-output')).toHaveCount(0);
    await page.getByTestId('git-connect-submit').click();
    await expect.poll(() => recorded.connected).toEqual([{ repositoryId: 1, settings: { ...settings, branch: '' } }]);

    await expect(page.getByTestId('site-build-reason-b1')).toContainText('cannot run as a server-rendered site');
    await page.getByTestId('site-build-container-b1').click();
    await expect(page).toHaveURL(/\/project\/test-project\/containers$/);
  });

  test('with the feature off the form offers only static sites', async ({ page }) => {
    await mockConfig(page, false);
    await mockSites(page);
    await page.goto('/project/test-project/sites/new');
    await expect(page.getByTestId('site-name')).toBeVisible();
    await expect(page.getByTestId('site-kind')).toHaveCount(0);
    await expect(page.getByTestId('site-spa-fallback')).toBeVisible();
  });
});
