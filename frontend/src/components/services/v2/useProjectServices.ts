import { useAppHostingEnabled, useSitesEnabled } from '../../../hooks/useDeploymentMode';
import { useInstance } from '../../../hooks/useProvisioning';
import { useApps, type App } from '../../../api/apps';
import { useSites, type Site } from '../../../api/sites';
import { useBuckets, type Bucket } from '../../../hooks/useStorage';
import { useEdgeFunctions } from '../../../hooks/useEdgeFunctions';
import type { DatabaseInstance } from '../../../types';
import type { ServiceRef } from './panelState';

export interface ProjectServices {
  // The project record, whether or not it has a database.
  readonly project?: DatabaseInstance;
  // Present only when the project has a database; a container-only project has none.
  readonly database?: DatabaseInstance;
  readonly apps: readonly App[];
  readonly sites: readonly Site[];
  readonly buckets: readonly Bucket[];
  readonly functionCount: number;
  readonly appHosting: boolean;
  readonly sitesOn: boolean;
  readonly loading: boolean;
  readonly error: unknown;
}

// Everything a project holds, from the same hooks the single-service pages use.
// A service the install does not offer is never asked for. Storage and
// functions are optional: a project that cannot read them simply has none to show.
export function useProjectServices(projectId: string): ProjectServices {
  const { enabled: appHosting, isLoading: hostingLoading } = useAppHostingEnabled();
  const { enabled: sitesOn, isLoading: sitesConfigLoading } = useSitesEnabled();
  const instance = useInstance(projectId);
  const apps = useApps(projectId, appHosting);
  const sites = useSites(sitesOn ? projectId : '');
  const buckets = useBuckets(projectId);
  const functions = useEdgeFunctions(projectId);
  const project = instance.data;

  return {
    project,
    database: project && !project.noDatabase ? project : undefined,
    apps: appHosting ? (apps.data ?? []) : [],
    sites: sitesOn ? (sites.data ?? []) : [],
    buckets: Array.isArray(buckets.data) ? buckets.data : [],
    functionCount: Array.isArray(functions.data) ? functions.data.length : 0,
    appHosting,
    sitesOn,
    loading: hostingLoading || sitesConfigLoading || instance.isLoading || apps.isLoading || sites.isLoading || buckets.isLoading || functions.isLoading,
    error: instance.error ?? apps.error ?? sites.error,
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
      return services.functionCount > 0;
  }
}

export const isEmptyProject = (services: ProjectServices): boolean =>
  !services.loading &&
  services.project !== undefined &&
  services.database === undefined &&
  services.apps.length === 0 &&
  services.sites.length === 0 &&
  services.buckets.length === 0 &&
  services.functionCount === 0;
