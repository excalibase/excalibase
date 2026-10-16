import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Sites (EXC-353): a project's built frontend, uploaded and served from the CDN.
export interface Site {
  id: string;
  projectId: string;
  name: string;
  hostname: string;
  // Unknown paths answer /index.html instead of the nearest 404.html.
  spaFallback: boolean;
  liveDeployId?: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  url: string;
}

export type SiteDeployStatus = 'PENDING' | 'SUCCEEDED' | 'FAILED';

export interface SiteDeploy {
  id: string;
  siteId: string;
  projectId: string;
  status: SiteDeployStatus;
  fileCount: number;
  totalBytes: number;
  // The routing this deploy was published with; a rollback brings it back.
  spaFallback: boolean;
  label?: string;
  failureReason?: string;
  createdBy: string;
  // The deployer's username; absent once their account is gone.
  createdByName?: string;
  createdAt: string;
  finishedAt?: string;
  live: boolean;
}

export interface SiteInput {
  name: string;
  spaFallback: boolean;
}

// The server may hold a deploy for up to 15 minutes while the CDN publishes it.
const PUBLISH_TIMEOUT_MS = 16 * 60 * 1000;

const sitesBase = (projectId: string) => `/projects/${projectId}/sites`;
const siteBase = (projectId: string, siteId: string) => `${sitesBase(projectId)}/${siteId}`;
const sitesKey = (projectId: string) => ['sites', projectId] as const;
const siteKey = (projectId: string, siteId: string) => ['sites', projectId, siteId] as const;
const deploysKey = (projectId: string, siteId: string) =>
  ['sites', projectId, siteId, 'deploys'] as const;

export const listSites = async (projectId: string): Promise<Site[]> =>
  (await api.get<Site[]>(`${sitesBase(projectId)}/`)).data;

export const getSite = async (projectId: string, siteId: string): Promise<Site> =>
  (await api.get<Site>(siteBase(projectId, siteId))).data;

export const createSite = async (projectId: string, input: SiteInput): Promise<Site> =>
  (await api.post<Site>(`${sitesBase(projectId)}/`, input)).data;

export const setSpaFallback = async (
  projectId: string,
  siteId: string,
  spaFallback: boolean,
): Promise<Site> => (await api.patch<Site>(siteBase(projectId, siteId), { spaFallback })).data;

export const deleteSite = async (projectId: string, siteId: string): Promise<void> => {
  await api.delete(siteBase(projectId, siteId));
};

export const listSiteDeploys = async (projectId: string, siteId: string): Promise<SiteDeploy[]> => {
  const { data } = await api.get<SiteDeploy[]>(`${siteBase(projectId, siteId)}/deploys`);
  return [...data].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
};

// The archive is the raw body, as `curl --data-binary @dist.tgz` sends it.
export const deploySite = async (
  projectId: string,
  siteId: string,
  archive: Blob,
): Promise<SiteDeploy> =>
  (
    await api.post<SiteDeploy>(`${siteBase(projectId, siteId)}/deploy`, archive, {
      headers: {
        'Content-Type':
          archive.type === 'application/gzip' ? 'application/gzip' : 'application/octet-stream',
      },
      timeout: PUBLISH_TIMEOUT_MS,
    })
  ).data;

export const rollbackSite = async (
  projectId: string,
  siteId: string,
  deployId: string,
): Promise<Site> =>
  (
    await api.post<Site>(`${siteBase(projectId, siteId)}/deploys/${deployId}/rollback`, undefined, {
      timeout: PUBLISH_TIMEOUT_MS,
    })
  ).data;

export const useSites = (projectId: string) =>
  useQuery({
    queryKey: sitesKey(projectId),
    queryFn: () => listSites(projectId),
    enabled: !!projectId,
  });

export const useSite = (projectId: string, siteId: string) =>
  useQuery({
    queryKey: siteKey(projectId, siteId),
    queryFn: () => getSite(projectId, siteId),
    enabled: !!projectId && !!siteId,
  });

export const useSiteDeploys = (projectId: string, siteId: string) =>
  useQuery({
    queryKey: deploysKey(projectId, siteId),
    queryFn: () => listSiteDeploys(projectId, siteId),
    enabled: !!projectId && !!siteId,
  });

export const useCreateSite = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: SiteInput) => createSite(projectId, input),
    onSettled: () => qc.invalidateQueries({ queryKey: sitesKey(projectId) }),
  });
};

// A deploy or rollback moves the live deploy, so the site and its history are read again.
const useSiteChange = <T>(
  projectId: string,
  siteId: string,
  change: (input: T) => Promise<unknown>,
) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: change,
    onSettled: () => qc.invalidateQueries({ queryKey: siteKey(projectId, siteId) }),
  });
};

export const useDeploySite = (projectId: string, siteId: string) =>
  useSiteChange(projectId, siteId, (archive: Blob) => deploySite(projectId, siteId, archive));

export const useRollbackSite = (projectId: string, siteId: string) =>
  useSiteChange(projectId, siteId, (deployId: string) => rollbackSite(projectId, siteId, deployId));

export const useSetSpaFallback = (projectId: string, siteId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (spaFallback: boolean) => setSpaFallback(projectId, siteId, spaFallback),
    onSuccess: (site) => qc.setQueryData(siteKey(projectId, siteId), site),
  });
};

export const useDeleteSite = (projectId: string, siteId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => deleteSite(projectId, siteId),
    onSuccess: () => {
      qc.removeQueries({ queryKey: siteKey(projectId, siteId) });
      return qc.invalidateQueries({ queryKey: sitesKey(projectId), exact: true });
    },
  });
};
