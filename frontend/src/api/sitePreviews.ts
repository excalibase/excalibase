import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Previews of git-connected sites (EXC-660): one per branch or pull request, at an unguessable link.
export type PreviewMode = 'off' | 'pull-requests' | 'branches';

export interface PreviewAccess {
  // Build pull requests from forks; their builds never see a project secret.
  forks: boolean;
  // Give same-repository preview builds the build-visible project secrets.
  secrets: boolean;
  // Put each preview's origin on the project's CORS list.
  apiAccess: boolean;
  // Post preview links on public repositories too.
  publicLinks: boolean;
}

export interface PreviewSettings extends PreviewAccess {
  mode: PreviewMode;
}

export type PreviewStatus = 'BUILDING' | 'READY' | 'FAILED' | 'REMOVING';

export interface SitePreview {
  id: string;
  siteId: string;
  source: 'pull-request' | 'branch';
  ref: string;
  hostname: string;
  status: PreviewStatus;
  commitSha: string;
  deployedSha?: string;
  failureReason?: string;
  fileCount: number;
  totalBytes: number;
  createdAt: string;
  updatedAt: string;
}

export interface PreviewOverview {
  settings: PreviewSettings;
  previews: SitePreview[];
  used: number;
  max: number;
}

const base = (projectId: string, siteId: string) => `/projects/${projectId}/sites/${siteId}/previews`;
const previewsKey = (projectId: string, siteId: string) => ['site-previews', projectId, siteId] as const;

export const getPreviews = async (projectId: string, siteId: string): Promise<PreviewOverview> =>
  (await api.get<PreviewOverview>(base(projectId, siteId))).data;

export const setPreviewMode = async (projectId: string, siteId: string, mode: PreviewMode): Promise<PreviewOverview> =>
  (await api.put<PreviewOverview>(`${base(projectId, siteId)}/mode`, { mode })).data;

export const setPreviewAccess = async (
  projectId: string,
  siteId: string,
  access: PreviewAccess,
): Promise<PreviewOverview> => (await api.put<PreviewOverview>(`${base(projectId, siteId)}/access`, access)).data;

export const removePreview = async (projectId: string, siteId: string, previewId: string): Promise<void> => {
  await api.delete(`${base(projectId, siteId)}/${previewId}`);
};

const BUSY = new Set<PreviewStatus>(['BUILDING', 'REMOVING']);

export const useSitePreviews = (projectId: string, siteId: string, enabled: boolean) =>
  useQuery({
    queryKey: previewsKey(projectId, siteId),
    queryFn: () => getPreviews(projectId, siteId),
    enabled: enabled && !!projectId && !!siteId,
    retry: false,
    refetchInterval: (query) => (query.state.data?.previews.some((preview) => BUSY.has(preview.status)) ? 5000 : false),
  });

function useOverviewMutation<T>(projectId: string, siteId: string, call: (input: T) => Promise<PreviewOverview>) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: call,
    onSuccess: (overview) => client.setQueryData(previewsKey(projectId, siteId), overview),
  });
}

export const useSetPreviewMode = (projectId: string, siteId: string) =>
  useOverviewMutation(projectId, siteId, (mode: PreviewMode) => setPreviewMode(projectId, siteId, mode));

export const useSetPreviewAccess = (projectId: string, siteId: string) =>
  useOverviewMutation(projectId, siteId, (access: PreviewAccess) => setPreviewAccess(projectId, siteId, access));

export const useRemovePreview = (projectId: string, siteId: string) => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (previewId: string) => removePreview(projectId, siteId, previewId),
    onSuccess: () => client.invalidateQueries({ queryKey: previewsKey(projectId, siteId) }),
  });
};
