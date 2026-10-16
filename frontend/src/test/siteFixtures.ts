import type { Site, SiteDeploy } from '../api/sites';

// Shared shapes for the Sites tests, as the server presents them.
export const siteFixture = (overrides: Partial<Site> = {}): Site => ({
  id: 'site-1',
  projectId: 'proj-1',
  name: 'shop',
  hostname: 'shop.example.app',
  spaFallback: true,
  liveDeployId: 'dep-2',
  createdBy: 'user-1',
  createdAt: '2026-10-10T10:00:00Z',
  updatedAt: '2026-10-10T10:00:00Z',
  url: 'https://shop.example.app',
  ...overrides,
});

export const siteDeployFixture = (overrides: Partial<SiteDeploy> = {}): SiteDeploy => ({
  id: 'dep-1',
  siteId: 'site-1',
  projectId: 'proj-1',
  status: 'SUCCEEDED',
  fileCount: 12,
  totalBytes: 1536,
  spaFallback: true,
  createdBy: 'user-1',
  createdAt: '2026-10-11T10:00:00Z',
  finishedAt: '2026-10-11T10:01:00Z',
  live: false,
  ...overrides,
});
