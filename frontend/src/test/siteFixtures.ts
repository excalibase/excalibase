import { useAuthStore } from '../stores/auth-store';
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

// The project proj-1 sits in org o1, where the signed-in member is a developer.
export const developerRole = (url: string): Promise<never> | undefined => {
  if (url === '/provision/proj-1')
    return Promise.resolve({ data: { projectId: 'proj-1', orgId: 'o1' } } as never);
  if (url === '/orgs')
    return Promise.resolve({
      data: [{ id: 'o1', name: 'O', slug: 'o', role: 'developer' }],
    } as never);
  return undefined;
};

export const signInAsMember = () =>
  useAuthStore.getState().setAuth({ id: 'u1', username: 'dev', email: 'd@x.test', role: 'user' });
