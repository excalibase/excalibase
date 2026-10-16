import { describe, test, expect, vi, beforeEach } from 'vitest';
import { api } from './client';
import {
  createDeployToken,
  createSite,
  deleteSite,
  getSiteLimits,
  listDeployTokens,
  revokeDeployToken,
  deploySite,
  listSiteDeploys,
  listSites,
  rollbackSite,
  setSpaFallback,
  type SiteDeploy,
} from './sites';

vi.mock('./client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const deploy = (id: string, createdAt: string): SiteDeploy => ({
  id,
  siteId: 'site-1',
  projectId: 'proj-1',
  status: 'SUCCEEDED',
  fileCount: 3,
  totalBytes: 2048,
  spaFallback: true,
  createdBy: 'user-1',
  createdAt,
  live: false,
});

describe('sites api', () => {
  beforeEach(() => vi.clearAllMocks());

  test('lists the project sites', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [{ id: 'site-1' }] } as never);
    await expect(listSites('proj-1')).resolves.toEqual([{ id: 'site-1' }]);
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/sites/');
  });

  test('creates a site with its name and routing', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { id: 'site-1' } } as never);
    await createSite('proj-1', { name: 'shop', spaFallback: false });
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/', {
      name: 'shop',
      spaFallback: false,
    });
  });

  test('changes the SPA fallback', async () => {
    vi.mocked(api.patch).mockResolvedValue({ data: { id: 'site-1', spaFallback: false } } as never);
    await setSpaFallback('proj-1', 'site-1', false);
    expect(api.patch).toHaveBeenCalledWith('/projects/proj-1/sites/site-1', { spaFallback: false });
  });

  test('deletes a site', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    await deleteSite('proj-1', 'site-1');
    expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/sites/site-1');
  });

  test('lists deploys newest first', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [deploy('old', '2026-10-01T00:00:00Z'), deploy('new', '2026-10-02T00:00:00Z')],
    } as never);
    const deploys = await listSiteDeploys('proj-1', 'site-1');
    expect(deploys.map((item) => item.id)).toEqual(['new', 'old']);
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/deploys');
  });

  test('uploads the archive as the raw body, with time for the CDN to publish it', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: deploy('d1', '2026-10-02T00:00:00Z') } as never);
    const archive = new Blob(['x'], { type: 'application/gzip' });
    await deploySite('proj-1', 'site-1', archive);
    const [url, body, config] = vi.mocked(api.post).mock.calls[0];
    expect(url).toBe('/projects/proj-1/sites/site-1/deploy');
    expect(body).toBe(archive);
    expect(config?.headers).toMatchObject({ 'Content-Type': 'application/gzip' });
    expect(config?.timeout).toBeGreaterThanOrEqual(15 * 60 * 1000);
  });

  test('sends a plain tar as octet-stream', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: deploy('d1', '2026-10-02T00:00:00Z') } as never);
    await deploySite('proj-1', 'site-1', new Blob(['x']));
    expect(vi.mocked(api.post).mock.calls[0][2]?.headers).toMatchObject({
      'Content-Type': 'application/octet-stream',
    });
  });

  test('rolls back to a deploy', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { id: 'site-1', liveDeployId: 'd1' } } as never);
    await rollbackSite('proj-1', 'site-1', 'd1');
    expect(api.post).toHaveBeenCalledWith(
      '/projects/proj-1/sites/site-1/deploys/d1/rollback',
      undefined,
      expect.anything(),
    );
  });
});

describe('sites limits and deploy tokens api (EXC-620)', () => {
  beforeEach(() => vi.clearAllMocks());

  test("reads the project's site limits", async () => {
    const limits = {
      maxSites: 5,
      maxDeployBytes: 104857600,
      maxFiles: 20000,
      maxFileBytes: 26214400,
      maxDeploysPerHour: 30,
    };
    vi.mocked(api.get).mockResolvedValue({ data: limits } as never);
    await expect(getSiteLimits('proj-1')).resolves.toEqual(limits);
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/sites/limits');
  });

  test("lists, creates and revokes a site's deploy tokens", async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [{ id: 'tok-1' }] } as never);
    await expect(listDeployTokens('proj-1', 'site-1')).resolves.toEqual([{ id: 'tok-1' }]);
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/tokens');

    vi.mocked(api.post).mockResolvedValue({ data: { id: 'tok-2', token: 'excd_secret' } } as never);
    await expect(createDeployToken('proj-1', 'site-1', 'github')).resolves.toEqual({
      id: 'tok-2',
      token: 'excd_secret',
    });
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/tokens', {
      name: 'github',
    });

    vi.mocked(api.delete).mockResolvedValue({} as never);
    await revokeDeployToken('proj-1', 'site-1', 'tok-2');
    expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/tokens/tok-2');
  });
});
