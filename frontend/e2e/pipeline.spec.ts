import { test, expect, type Page } from '@playwright/test';
import { loginAs, mockProject, mockSchemaEndpoints } from './helpers';

const DIGEST_OLD = `sha256:${'11'.repeat(32)}`;
const DIGEST_NEW = `sha256:${'22'.repeat(32)}`;
const APP_PATH = '/api/projects/test-project/apps/app-1';

const app = {
  id: 'app-1', projectId: 'test-project', name: 'web', image: 'ghcr.io/acme/web:main', env: [],
  port: 8080, replicas: 1, tier: 'STANDARD', status: 'ACTIVE', version: 4,
  createdAt: '2026-10-01T10:00:00Z', updatedAt: '2026-10-01T10:00:00Z',
};

const deploys = [
  {
    id: 'dep-2', appId: 'app-1', projectId: 'test-project', revision: 2, status: 'succeeded',
    image: `ghcr.io/acme/web@${DIGEST_NEW}`, imageRef: 'ghcr.io/acme/web:main', digest: DIGEST_NEW,
    source: 'api', commitSha: '9fceb02d0ae598e95dc970b74767f19372d61af8',
    createdBy: 'u1', createdAt: '2026-10-06T10:00:00Z', finishedAt: '2026-10-06T10:00:42Z',
  },
  {
    id: 'dep-1', appId: 'app-1', projectId: 'test-project', revision: 1, status: 'succeeded',
    image: `ghcr.io/acme/web@${DIGEST_OLD}`, imageRef: 'ghcr.io/acme/web:main', digest: DIGEST_OLD,
    source: 'image-watcher', createdBy: 'image-watcher', createdAt: '2026-10-05T10:00:00Z',
    finishedAt: '2026-10-05T10:01:05Z',
  },
];

interface Recorded {
  patches: Array<{ body: unknown; ifMatch?: string }>;
  redeploys: string[];
}

async function mockPipeline(page: Page): Promise<Recorded> {
  const recorded: Recorded = { patches: [], redeploys: [] };
  let current = { ...app } as Record<string, unknown>;
  await page.route('**/api/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deploymentMode: 'cloud', appHosting: true }) }),
  );
  await page.route(new RegExp(`${APP_PATH}$`), async (route) => {
    if (route.request().method() === 'PATCH') {
      recorded.patches.push({ body: route.request().postDataJSON(), ifMatch: route.request().headers()['if-match'] });
      current = { ...current, ...route.request().postDataJSON(), version: (current.version as number) + 1 };
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(current) });
  });
  await page.route(new RegExp(`${APP_PATH}/deploys(\\?.*)?$`), (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(deploys) }),
  );
  await page.route(new RegExp(`${APP_PATH}/deploys/[^/]+/redeploy$`), (route) => {
    recorded.redeploys.push(route.request().url().split('/').at(-2) ?? '');
    return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ ...deploys[1], id: 'dep-3', revision: 3, status: 'pending' }) });
  });
  await page.route(new RegExp(`${APP_PATH}/logs`), (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ lines: [] }) }),
  );
  return recorded;
}

test.describe('Container pipeline', () => {
  test.beforeEach(async ({ page }) => {
    await loginAs(page);
    await mockProject(page);
    await mockSchemaEndpoints(page);
  });

  test('shows each deploy with its source, commit, digest and duration', async ({ page }) => {
    await mockPipeline(page);
    await page.goto('/project/test-project/containers/app-1/pipeline');
    const ci = page.getByTestId('pipeline-deploy-dep-2');
    await expect(ci.getByTestId('deploy-source')).toHaveText('CI · 9fceb02');
    await expect(ci.getByTestId('deploy-image')).toContainText('222222222222');
    await expect(ci.getByTestId('deploy-duration')).toHaveText('42s');
    await expect(page.getByTestId('pipeline-deploy-dep-1').getByTestId('deploy-source')).toHaveText('Image watcher');
  });

  test('rolls back to the older digest after confirming', async ({ page }) => {
    const recorded = await mockPipeline(page);
    await page.goto('/project/test-project/containers/app-1/pipeline');
    await page.getByTestId('rollback-dep-1').click();
    await expect(page.getByTestId('rollback-confirm-text')).toContainText('Database migrations are not touched');
    await page.getByTestId('rollback-confirm-dep-1').click();
    await expect.poll(() => recorded.redeploys).toEqual(['dep-1']);
  });

  test('switches auto-deploy on for the tag', async ({ page }) => {
    const recorded = await mockPipeline(page);
    await page.goto('/project/test-project/containers/app-1/pipeline');
    await page.getByTestId('auto-deploy-toggle').click();
    await expect.poll(() => recorded.patches).toEqual([{ body: { autoDeploy: true }, ifMatch: '4' }]);
    await expect(page.getByTestId('auto-deploy-toggle')).toBeChecked();
    await expect(page.getByTestId('image-watch')).toContainText('ghcr.io/acme/web:main');
  });

  test('gives a GitHub Actions workflow filled in for this app', async ({ page }) => {
    await mockPipeline(page);
    await page.goto('/project/test-project/containers/app-1/pipeline');
    const snippet = page.getByTestId('ci-snippet');
    await expect(snippet).toContainText('docker/build-push-action@v6');
    await expect(snippet).toContainText("/projects/test-project/apps/app-1\"");
    await expect(snippet).toContainText('${{ secrets.EXCALIBASE_TOKEN }}');
  });

  test('gives GitLab CI, Jenkins and plain curl setups too', async ({ page }) => {
    await mockPipeline(page);
    await page.goto('/project/test-project/containers/app-1/pipeline');
    const snippet = page.getByTestId('ci-snippet');
    await page.getByTestId('ci-tab-gitlab').click();
    await expect(snippet).toContainText('docker:27-dind');
    await expect(page.getByTestId('ci-file')).toHaveText('.gitlab-ci.yml');
    await page.getByTestId('ci-tab-jenkins').click();
    await expect(snippet).toContainText("credentials('excalibase-token')");
    await page.getByTestId('ci-tab-curl').click();
    await expect(snippet).toContainText('COMMIT_SHA');
    await expect(snippet).toContainText('/projects/test-project/apps/app-1');
  });

  test('is reached from the container page', async ({ page }) => {
    await mockPipeline(page);
    await page.route(new RegExp(`${APP_PATH}/(domains/|certificate|disk)$`), (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    );
    await page.goto('/project/test-project/containers/app-1');
    await page.getByTestId('pipeline-link').click();
    await expect(page.getByTestId('container-pipeline-page')).toBeVisible();
  });
});
