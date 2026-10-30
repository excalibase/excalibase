import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockProject, mockProjectRole, mockSchemaEndpoints } from './helpers';

// Previews of a git-connected site (EXC-660): shown only while /api/config reports sitepreviews.
const SITE_PATH = '/api/projects/test-project/sites';
const HOST = 'shop--abcdefghijklmnopqrstuvwxyz.example.app';

interface Recorded {
  modes: unknown[];
  access: unknown[];
  removed: string[];
}

const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function mockConfig(page: Page, previews: boolean) {
  await page.route('**/api/config', (route) =>
    json(route, {
      deploymentMode: 'cloud',
      appHosting: true,
      sites: true,
      siteGitBuilds: true,
      features: { sitepreviews: previews },
    }),
  );
}

async function mockSite(page: Page): Promise<Recorded> {
  const recorded: Recorded = { modes: [], access: [], removed: [] };
  let settings = { mode: 'off', forks: false, secrets: false, apiAccess: false, publicLinks: false };
  let previews = [
    {
      id: 'pv-1',
      siteId: 'site-1',
      source: 'pull-request',
      ref: '7',
      hostname: HOST,
      status: 'READY',
      commitSha: 'abcdef1234567890abcdef1234567890abcdef12',
      fileCount: 3,
      totalBytes: 2048,
      createdAt: '2026-10-30T10:00:00Z',
      updatedAt: '2026-10-30T10:05:00Z',
    },
  ];
  const overview = () => ({ settings, previews, used: previews.length, max: 2 });
  const site = {
    id: 'site-1',
    projectId: 'test-project',
    name: 'shop',
    hostname: 'shop.example.app',
    url: 'https://shop.example.app',
    spaFallback: true,
  };
  await page.route(new RegExp(`${SITE_PATH}(/.*)?$`), async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(SITE_PATH, '');
    const method = request.method();
    if (path === '/site-1' && method === 'GET') return json(route, site);
    if (path === '/site-1/deploys' || path === '/site-1/builds' || path === '/site-1/tokens' || path === '/site-1/domains')
      return json(route, []);
    if (path === '/build-secrets') return json(route, []);
    if (path === '/site-1/git')
      return json(route, {
        connected: true,
        nodeVersions: ['22'],
        connection: {
          siteId: 'site-1',
          provider: 'github',
          repository: 'acme/shop',
          repositoryId: 1,
          settings: { branch: 'main', rootDir: '', installCommand: '', buildCommand: '', outputDir: 'dist', nodeVersion: '22', env: {} },
          connectedBy: 'u1',
          createdAt: '2026-10-30T09:00:00Z',
          updatedAt: '2026-10-30T09:00:00Z',
        },
      });
    if (path === '/site-1/previews' && method === 'GET') return json(route, overview());
    if (path === '/site-1/previews/mode' && method === 'PUT') {
      const body = request.postDataJSON() as { mode: string };
      recorded.modes.push(body);
      settings = { ...settings, mode: body.mode };
      if (body.mode === 'off') previews = [];
      return json(route, overview());
    }
    if (path === '/site-1/previews/access' && method === 'PUT') {
      const body = request.postDataJSON() as typeof settings;
      recorded.access.push(body);
      settings = { ...settings, ...body };
      return json(route, overview());
    }
    const removed = /^\/site-1\/previews\/([^/]+)$/.exec(path);
    if (removed && method === 'DELETE') {
      recorded.removed.push(removed[1]);
      previews = previews.filter((preview) => preview.id !== removed[1]);
      return route.fulfill({ status: 204 });
    }
    return json(route, { error: `unmocked ${method} ${path}` }, 404);
  });
  return recorded;
}

test.describe('Site previews', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('an admin turns previews on, opens a preview link, allows forks and removes a preview', async ({ page }) => {
    await mockProjectRole(page, 'admin');
    await mockConfig(page, true);
    const recorded = await mockSite(page);

    await page.goto('/project/test-project/sites/site-1');
    await expect(page.getByTestId('site-previews')).toBeVisible();
    await page.getByTestId('preview-mode').selectOption('pull-requests');
    await expect.poll(() => recorded.modes).toEqual([{ mode: 'pull-requests' }]);

    const link = page.getByTestId('preview-link-pv-1');
    await expect(link).toHaveAttribute('href', `https://${HOST}`);
    await expect(link).toHaveAttribute('rel', /noreferrer/);
    await expect(page.getByTestId('previews-usage')).toContainText('1 of 2');

    // The box follows the server's answer, so it shows what is really saved.
    await page.getByTestId('preview-access-forks').click();
    await expect
      .poll(() => recorded.access)
      .toEqual([{ forks: true, secrets: false, apiAccess: false, publicLinks: false }]);
    await expect(page.getByTestId('preview-access-forks')).toBeChecked();

    await page.getByTestId('preview-remove-pv-1').click();
    await expect.poll(() => recorded.removed).toEqual(['pv-1']);
    await expect(page.getByTestId('preview-pv-1')).toHaveCount(0);
  });

  test('a developer sets the mode but cannot change what previews may reach', async ({ page }) => {
    await mockProjectRole(page, 'developer');
    await mockConfig(page, true);
    await mockSite(page);

    await page.goto('/project/test-project/sites/site-1');
    await expect(page.getByTestId('preview-mode')).toBeEnabled();
    await expect(page.getByTestId('preview-access-secrets')).toBeDisabled();
    await expect(page.getByTestId('preview-access-note')).toBeVisible();
  });

  test('with the feature off the site shows no previews and never asks for them', async ({ page }) => {
    await mockProjectRole(page, 'admin');
    await mockConfig(page, false);
    await mockSite(page);
    const asked: string[] = [];
    page.on('request', (request) => {
      if (request.url().includes('/previews')) asked.push(request.url());
    });

    await page.goto('/project/test-project/sites/site-1');
    await expect(page.getByTestId('site-git-builds')).toBeVisible();
    await expect(page.getByTestId('site-previews')).toHaveCount(0);
    expect(asked).toEqual([]);
  });
});
