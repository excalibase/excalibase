import { test, expect, type Page, type Route } from '@playwright/test';
import { loginAs, mockProject, mockProjectRole, mockSchemaEndpoints } from './helpers';

// Sites (EXC-353): static frontend hosting, shown only when /api/config reports sites.
const SITES_PATH = '/api/projects/test-project/sites';

interface SiteState {
  id: string;
  name: string;
  hostname: string;
  url: string;
  spaFallback: boolean;
  liveDeployId?: string;
}

interface DeployState {
  id: string;
  status: 'SUCCEEDED' | 'FAILED' | 'PENDING';
  fileCount: number;
  totalBytes: number;
  spaFallback: boolean;
  createdBy: string;
  createdAt: string;
  label?: string;
}

interface Recorded {
  created: unknown[];
  uploads: Array<{ contentType?: string; bytes: number }>;
  rollbacks: string[];
  patches: unknown[];
  deleted: boolean;
}

async function mockConfig(page: Page, sites: boolean) {
  await page.route('**/api/config', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentMode: 'cloud', appHosting: true, sites, features: {} }),
    }),
  );
}

const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

// A stateful stand-in for the Sites API: create, upload, roll back, toggle, delete.
async function mockSitesApi(
  page: Page,
  initialDeploys: DeployState[] = [],
  initialSite: SiteState | null = null,
): Promise<Recorded> {
  const recorded: Recorded = {
    created: [],
    uploads: [],
    rollbacks: [],
    patches: [],
    deleted: false,
  };
  let site: SiteState | null = initialSite;
  let deploys = [...initialDeploys];
  const present = () => ({
    ...site,
    projectId: 'test-project',
    createdBy: 'u1',
    createdAt: '2026-10-16T10:00:00Z',
    updatedAt: '2026-10-16T10:00:00Z',
  });
  const presentDeploys = () =>
    deploys.map((deploy) => ({
      ...deploy,
      siteId: 'site-1',
      projectId: 'test-project',
      live: deploy.id === site?.liveDeployId,
    }));

  await page.route(new RegExp(`${SITES_PATH}(/.*)?$`), async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(SITES_PATH, '');
    const method = request.method();
    if (path === '/' && method === 'GET') return json(route, site ? [present()] : []);
    if (path === '/' && method === 'POST') {
      const body = request.postDataJSON() as { name: string; spaFallback: boolean };
      recorded.created.push(body);
      site = {
        id: 'site-1',
        name: body.name,
        hostname: `${body.name}.example.app`,
        url: `https://${body.name}.example.app`,
        spaFallback: body.spaFallback,
      };
      return json(route, present(), 201);
    }
    if (path === '/site-1' && method === 'GET')
      return site ? json(route, present()) : json(route, { error: 'not found' }, 404);
    if (path === '/site-1' && method === 'PATCH') {
      const body = request.postDataJSON() as { spaFallback: boolean };
      recorded.patches.push(body);
      site = site && { ...site, spaFallback: body.spaFallback };
      return json(route, present());
    }
    if (path === '/site-1' && method === 'DELETE') {
      recorded.deleted = true;
      site = null;
      return route.fulfill({ status: 204 });
    }
    if (path === '/site-1/deploys') return json(route, [...presentDeploys()].reverse());
    if (path === '/site-1/deploy' && method === 'POST') {
      recorded.uploads.push({
        contentType: request.headers()['content-type'],
        bytes: request.postDataBuffer()?.length ?? 0,
      });
      const deploy: DeployState = {
        id: `dep-${deploys.length + 1}`,
        status: 'SUCCEEDED',
        fileCount: 2,
        totalBytes: 4096,
        spaFallback: site?.spaFallback ?? true,
        createdBy: 'u1',
        createdAt: `2026-10-16T1${deploys.length + 1}:00:00Z`,
      };
      deploys = [...deploys, deploy];
      site = site && { ...site, liveDeployId: deploy.id };
      return json(
        route,
        { ...deploy, siteId: 'site-1', projectId: 'test-project', live: true },
        201,
      );
    }
    const rollback = /^\/site-1\/deploys\/([^/]+)\/rollback$/.exec(path);
    if (rollback && method === 'POST') {
      recorded.rollbacks.push(rollback[1]);
      site = site && { ...site, liveDeployId: rollback[1] };
      return json(route, present());
    }
    return json(route, { error: `unmocked ${method} ${path}` }, 500);
  });
  return recorded;
}

test.describe('Sites', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('create a site, deploy a build, roll back, change routing and delete it', async ({
    page,
  }) => {
    await mockProjectRole(page, 'developer');
    await mockConfig(page, true);
    const recorded = await mockSitesApi(page);

    await page.goto('/project/test-project');
    await page.getByTestId('nav-sites').click();
    await expect(page.getByTestId('sites-empty')).toBeVisible();

    await page.getByTestId('sites-new').click();
    await page.getByTestId('site-name').fill('studio');
    await expect(page.getByTestId('site-name-error')).toContainText('reserved');
    await expect(page.getByTestId('site-create')).toBeDisabled();
    await page.getByTestId('site-name').fill('shop');
    await page.getByTestId('site-create').click();
    await expect.poll(() => recorded.created).toEqual([{ name: 'shop', spaFallback: true }]);

    await expect(page.getByTestId('site-url')).toHaveAttribute('href', 'https://shop.example.app');
    await expect(page.getByTestId('site-deploys-empty')).toBeVisible();
    await expect(page.getByTestId('site-ci-snippet')).toContainText(
      '/projects/test-project/sites/site-1/deploy',
    );

    const archive = {
      name: 'dist.tgz',
      mimeType: 'application/gzip',
      buffer: Buffer.from([0x1f, 0x8b, 0x08, 0x00]),
    };
    await page.getByTestId('site-upload-file').setInputFiles(archive);
    await expect(page.getByTestId('site-upload-done')).toBeVisible();
    await page.getByTestId('site-upload-file').setInputFiles({ ...archive, name: 'dist2.tgz' });
    await expect(page.getByTestId('site-deploy-status-dep-2')).toHaveText('Live');
    expect(recorded.uploads).toEqual([
      { contentType: 'application/gzip', bytes: 4 },
      { contentType: 'application/gzip', bytes: 4 },
    ]);

    await page.getByTestId('site-rollback-dep-1').click();
    await page.getByTestId('site-rollback-confirm-dep-1').click();
    await expect(page.getByTestId('site-deploy-status-dep-1')).toHaveText('Live');
    expect(recorded.rollbacks).toEqual(['dep-1']);

    await page.getByTestId('site-spa-toggle').uncheck();
    await expect(page.getByTestId('site-spa-toggle')).not.toBeChecked();
    await expect.poll(() => recorded.patches).toEqual([{ spaFallback: false }]);

    await page.getByTestId('site-delete').click();
    await expect(page.getByTestId('modal-confirm')).toBeDisabled();
    await page.getByTestId('confirm-input').fill('shop');
    await page.getByTestId('modal-confirm').click();
    await expect(page.getByTestId('sites-empty')).toBeVisible();
    await expect.poll(() => recorded.deleted).toBe(true);
  });

  // The server refuses a viewer every change (403); Studio offers none.
  test('a viewer sees the site, its routing and history, and no control that changes it', async ({
    page,
  }) => {
    await mockProjectRole(page, 'viewer');
    await mockConfig(page, true);
    const deploy = (id: string, createdAt: string): DeployState => ({
      id,
      status: 'SUCCEEDED',
      fileCount: 3,
      totalBytes: 2048,
      spaFallback: true,
      createdBy: 'dev',
      createdAt,
    });
    await mockSitesApi(
      page,
      [deploy('dep-2', '2026-10-12T10:00:00Z'), deploy('dep-1', '2026-10-11T10:00:00Z')],
      {
        id: 'site-1',
        name: 'shop',
        hostname: 'shop.example.app',
        url: 'https://shop.example.app',
        spaFallback: true,
        liveDeployId: 'dep-2',
      },
    );
    await page.goto('/project/test-project/sites');
    await expect(page.getByTestId('site-row-site-1')).toBeVisible();
    await expect(page.getByTestId('sites-new')).toHaveCount(0);

    await page.goto('/project/test-project/sites/site-1');
    await expect(page.getByTestId('site-deploy-row-dep-1')).toBeVisible();
    await expect(page.getByTestId('site-spa-state')).toContainText('on');
    for (const id of ['site-upload', 'site-delete', 'site-spa-toggle', 'site-rollback-dep-1']) {
      await expect(page.getByTestId(id)).toHaveCount(0);
    }

    await page.goto('/project/test-project/sites/new');
    await expect(page.getByTestId('site-create-not-allowed')).toBeVisible();
    await expect(page.getByTestId('site-create')).toHaveCount(0);
  });

  test('an install without Sites hides the tab and points frontends at containers', async ({
    page,
  }) => {
    await mockConfig(page, false);
    await page.goto('/project/test-project');
    await expect(page.getByTestId('nav-containers')).toBeVisible();
    await expect(page.getByTestId('nav-sites')).toHaveCount(0);
    await expect(page.getByTestId('sites-unavailable')).toContainText(
      "Static frontend hosting isn't available on this install; deploy your frontend as a container.",
    );

    await page.goto('/project/test-project/sites');
    await expect(page.getByTestId('sites-unavailable')).toBeVisible();
    await expect(page.getByTestId('sites-new')).toHaveCount(0);
  });
});
