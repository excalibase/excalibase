import { useAppHostingEnabled, useSitesEnabled } from '../../../hooks/useDeploymentMode';
import { useInstance } from '../../../hooks/useProvisioning';
import { useApps, type App } from '../../../api/apps';
import { useSites, type Site } from '../../../api/sites';
import { useBuckets, type Bucket } from '../../../hooks/useStorage';
import { isNotOffered } from '../../../utils/notOffered';
import { useEdgeFunctions } from '../../../hooks/useEdgeFunctions';
import type { DatabaseInstance } from '../../../types';
import type { EdgeFunction } from '../../../types/edgefn';
import type { ServiceRef } from './panelState';

export interface ProjectServices {
  // The project record, whether or not it has a database.
  readonly project?: DatabaseInstance;
  // Present only when the project has a database; a container-only project has none.
  readonly database?: DatabaseInstance;
  readonly apps: readonly App[];
  readonly sites: readonly Site[];
  readonly buckets: readonly Bucket[];
  readonly functions: readonly EdgeFunction[];
  readonly appHosting: boolean;
  readonly sitesOn: boolean;
  // False when the install does not mount storage: its list answers 404.
  readonly storageOffered: boolean;
  // False when the install does not mount edge functions: its list answers 404.
  readonly functionsOffered: boolean;
  readonly loading: boolean;
  readonly error: unknown;
}

// Everything a project holds, from the same hooks the single-service pages use.
// A service the install does not offer is never asked for. Edge Functions are
// A request that failed is reported in `error`, never read as "none"; an install
// without the functions runtime answers 404, which is "not offered".
export function useProjectServices(projectId: string): ProjectServices {
  const { enabled: appHosting, isLoading: hostingLoading } = useAppHostingEnabled();
  const { enabled: sitesOn, isLoading: sitesConfigLoading } = useSitesEnabled();
  const instance = useInstance(projectId);
  const apps = useApps(projectId, appHosting);
  const sites = useSites(sitesOn ? projectId : '');
  const buckets = useBuckets(projectId);
  const functions = useEdgeFunctions(projectId);
  const project = instance.data;
  const storageOffered = !isNotOffered(buckets.error);
  const functionsOffered = !functions.isLoading && !isNotOffered(functions.error);

  return {
    project,
    database: project && !project.noDatabase ? project : undefined,
    apps: appHosting ? (apps.data ?? []) : [],
    sites: sitesOn ? (sites.data ?? []) : [],
    buckets: Array.isArray(buckets.data) ? buckets.data : [],
    functions: Array.isArray(functions.data) ? functions.data : [],
    appHosting,
    sitesOn,
    storageOffered,
    functionsOffered,
    loading: hostingLoading || sitesConfigLoading || instance.isLoading || apps.isLoading || sites.isLoading || buckets.isLoading || functions.isLoading,
    error: instance.error ?? apps.error ?? sites.error ?? (storageOffered ? buckets.error : undefined) ?? (isNotOffered(functions.error) ? undefined : functions.error),
  };
}

// Whether the project really holds the service a link names.
export function hasService(services: ProjectServices, ref: ServiceRef): boolean {
  switch (ref.kind) {
    case 'database':
      return services.database !== undefined;
    case 'app':
      return services.apps.some((app) => app.id === ref.id);
    case 'site':
      return services.sites.some((site) => site.id === ref.id);
    case 'storage':
      return services.buckets.some((bucket) => bucket.name === ref.id);
    case 'functions':
      return services.functionsOffered;
  }
}

export const isEmptyProject = (services: ProjectServices): boolean =>
  !services.loading &&
  services.error == null &&
  services.project !== undefined &&
  services.database === undefined &&
  services.apps.length === 0 &&
  services.sites.length === 0 &&
  services.buckets.length === 0 &&
  services.functions.length === 0;
