import { useQueries } from '@tanstack/react-query';
import { listApps, type App } from '../../api/apps';
import { listSites, type Site } from '../../api/sites';
import { allowedSourcesQuery, type AllowedSources } from '../../api/allowedSources';
import { useAppHostingEnabled, useFeatureEnabled, useSitesEnabled } from '../../hooks/useDeploymentMode';
import type { DatabaseInstance } from '../../types';

const GONE = new Set(['PENDING_DELETION', 'DELETING', 'DEPROVISIONED', 'BACKUPS_PENDING_DELETE']);
const STALE_MS = 30_000;

// A list is absent while it loads or when it failed (`failed` tells which); it is empty when the install does not offer it.
// What an install that does not offer a service holds of it: none, once it is known not to offer it.
const offered = (configLoading: boolean): never[] | undefined => (configLoading ? undefined : []);

export interface LoadedServices { apps?: App[]; sites?: Site[]; appsFailed?: boolean; sitesFailed?: boolean }

// The apps and sites of each project, from the lists their own pages read (same cache keys).
// A list that could not be read is absent, never an empty one.
export function useProjectServices(projects: readonly DatabaseInstance[]): { byProject: Map<string, LoadedServices>; isLoading: boolean } {
  const { enabled: appsOn, isLoading: appsConfigLoading } = useAppHostingEnabled();
  const { enabled: sitesOn, isLoading: sitesConfigLoading } = useSitesEnabled();
  const live = projects.filter((p) => !GONE.has(p.status));
  const apps = useQueries({
    queries: live.map((p) => ({ queryKey: ['apps', p.projectId], queryFn: () => listApps(p.projectId), enabled: appsOn, retry: false, staleTime: STALE_MS })),
  });
  const sites = useQueries({
    queries: live.map((p) => ({ queryKey: ['sites', p.projectId], queryFn: () => listSites(p.projectId), enabled: sitesOn, retry: false, staleTime: STALE_MS })),
  });
  const byProject = new Map<string, LoadedServices>();
  live.forEach((p, i) => byProject.set(p.projectId, {
    apps: appsOn ? apps[i].data : offered(appsConfigLoading),
    sites: sitesOn ? sites[i].data : offered(sitesConfigLoading),
    appsFailed: appsOn && apps[i].isError,
    sitesFailed: sitesOn && sites[i].isError,
  }));
  return { byProject, isLoading: apps.some((q) => q.isLoading) || sites.some((q) => q.isLoading) };
}

export interface OpenDatabases {
  // null: the platform does not report who may reach the databases, so nothing is claimed.
  open: number | null;
  unchecked: number;
}

// A database is open to the internet when its public port is on and any address may reach it:
// chosen so ("any"), or never narrowed ("unset", a port opened before allow-lists).
export function isOpenToInternet(sources: Pick<AllowedSources, 'publicEnabled' | 'access'>): boolean {
  return sources.publicEnabled && (sources.access === 'any' || sources.access === 'unset');
}

export function useOpenDatabases(projects: readonly DatabaseInstance[]): OpenDatabases & { isLoading: boolean } {
  const reported = useFeatureEnabled('dballowlist').enabled;
  const withDatabase = projects.filter((p) => p.status === 'ACTIVE' && p.noDatabase !== true);
  const results = useQueries({
    queries: withDatabase.map((p) => allowedSourcesQuery(p.projectId, reported)),
  });
  if (!reported) return { open: null, unchecked: 0, isLoading: false };
  return {
    open: results.filter((q) => q.data && isOpenToInternet(q.data)).length,
    unchecked: results.filter((q) => q.isError).length,
    isLoading: results.some((q) => q.isLoading),
  };
}
