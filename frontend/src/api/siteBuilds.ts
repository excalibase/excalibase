import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Git builds of sites (EXC-354): a push to the tracked branch builds the site in a sandbox and deploys it.
export interface SiteBuildSettings {
  branch: string;
  rootDir: string;
  installCommand: string;
  buildCommand: string;
  outputDir: string;
  nodeVersion: string;
  // Plain build variables; they end up in the built files.
  env: Record<string, string>;
}

export interface SiteGitConnection {
  siteId: string;
  provider: string;
  repository: string;
  repositoryId: number;
  settings: SiteBuildSettings;
  connectedBy: string;
  // The connector's username; builds read the repository under their GitHub sign-in.
  connectedByName?: string;
  // Set once the connector lost the developer rung; a developer has to connect again.
  inactiveReason?: string;
  createdAt: string;
  updatedAt: string;
}

export interface SiteGitState {
  connected: boolean;
  connection: SiteGitConnection | null;
  nodeVersions: string[];
}

export type SiteBuildStatus = 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'CANCELLED';

export interface SiteBuild {
  id: string;
  siteId: string;
  status: SiteBuildStatus;
  trigger: 'push' | 'rebuild';
  commitSha: string;
  commitMessage?: string;
  repository: string;
  settings: SiteBuildSettings;
  limits: { cpuMillis: number; memory: string; disk: string; timeoutSeconds: number };
  deployId?: string;
  failureKind?: string;
  failureReason?: string;
  log?: string;
  requestedBy: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface GitRepository {
  id: number;
  fullName: string;
  defaultBranch: string;
  private: boolean;
}

export interface GitAccess {
  installation: { id: number; account: string };
  repositories: GitRepository[];
}

export interface ConnectResult {
  connection: SiteGitConnection;
  build?: SiteBuild;
  buildError?: string;
}

export interface BuildSecret {
  name: string;
  buildVisible: boolean;
}

export const EMPTY_SETTINGS: SiteBuildSettings = {
  branch: '',
  rootDir: '',
  installCommand: '',
  buildCommand: '',
  outputDir: 'dist',
  nodeVersion: '22',
  env: {},
};

const siteBase = (projectId: string, siteId: string) => `/projects/${projectId}/sites/${siteId}`;
const gitKey = (projectId: string, siteId: string) => ['site-git', projectId, siteId] as const;
const buildsKey = (projectId: string, siteId: string) =>
  ['site-builds', projectId, siteId] as const;
const secretsKey = (projectId: string) => ['site-build-secrets', projectId] as const;

const ACTIVE = new Set<SiteBuildStatus>(['QUEUED', 'RUNNING']);
export const isActive = (build: SiteBuild) => ACTIVE.has(build.status);

export const getGitState = async (projectId: string, siteId: string): Promise<SiteGitState> =>
  (await api.get<SiteGitState>(`${siteBase(projectId, siteId)}/git`)).data;

export const connectRepository = async (
  projectId: string,
  siteId: string,
  input: { repositoryId: number; settings: SiteBuildSettings },
): Promise<ConnectResult> =>
  (await api.put<ConnectResult>(`${siteBase(projectId, siteId)}/git`, input)).data;

export const disconnectRepository = async (projectId: string, siteId: string): Promise<void> => {
  await api.delete(`${siteBase(projectId, siteId)}/git`);
};

// install opens GitHub's install page for the App; otherwise its sign-in.
export const startGitSignIn = async (
  projectId: string,
  siteId: string,
  install: boolean,
): Promise<string> =>
  (await api.post<{ url: string }>(`${siteBase(projectId, siteId)}/git/sign-in`, { install })).data
    .url;

export const listGitRepositories = async (
  projectId: string,
  siteId: string,
): Promise<GitAccess[]> =>
  (await api.get<GitAccess[]>(`${siteBase(projectId, siteId)}/git/repositories`)).data;

export const listSiteBuilds = async (projectId: string, siteId: string): Promise<SiteBuild[]> =>
  (await api.get<SiteBuild[]>(`${siteBase(projectId, siteId)}/builds`)).data;

export const getSiteBuild = async (
  projectId: string,
  siteId: string,
  buildId: string,
): Promise<SiteBuild> =>
  (await api.get<SiteBuild>(`${siteBase(projectId, siteId)}/builds/${buildId}`)).data;

export const rebuildSite = async (projectId: string, siteId: string): Promise<SiteBuild> =>
  (await api.post<SiteBuild>(`${siteBase(projectId, siteId)}/builds`)).data;

export const listBuildSecrets = async (projectId: string): Promise<BuildSecret[]> =>
  (await api.get<BuildSecret[]>(`/projects/${projectId}/sites/build-secrets`)).data;

export const setBuildSecrets = async (
  projectId: string,
  buildVisible: string[],
): Promise<BuildSecret[]> =>
  (await api.put<BuildSecret[]>(`/projects/${projectId}/sites/build-secrets`, { buildVisible }))
    .data;

export const useGitState = (projectId: string, siteId: string, enabled = true) =>
  useQuery({
    queryKey: gitKey(projectId, siteId),
    queryFn: () => getGitState(projectId, siteId),
    enabled: enabled && !!projectId && !!siteId,
    retry: false,
  });

export const useGitRepositories = (projectId: string, siteId: string, enabled: boolean) =>
  useQuery({
    queryKey: ['site-git-repositories', projectId, siteId],
    queryFn: () => listGitRepositories(projectId, siteId),
    enabled: enabled && !!projectId && !!siteId,
    retry: false,
  });

// Polls while a build is queued or running, so its status and log follow along.
export const useSiteBuilds = (projectId: string, siteId: string, enabled = true) =>
  useQuery({
    queryKey: buildsKey(projectId, siteId),
    queryFn: () => listSiteBuilds(projectId, siteId),
    enabled: enabled && !!projectId && !!siteId,
    refetchInterval: (query) => (query.state.data?.some(isActive) ? 5000 : false),
  });

export const useSiteBuild = (projectId: string, siteId: string, buildId: string | null) =>
  useQuery({
    queryKey: [...buildsKey(projectId, siteId), buildId],
    queryFn: () => getSiteBuild(projectId, siteId, buildId ?? ''),
    enabled: !!buildId,
    refetchInterval: (query) => (query.state.data && isActive(query.state.data) ? 5000 : false),
  });

export const useConnectRepository = (projectId: string, siteId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { repositoryId: number; settings: SiteBuildSettings }) =>
      connectRepository(projectId, siteId, input),
    onSettled: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: gitKey(projectId, siteId) }),
        qc.invalidateQueries({ queryKey: buildsKey(projectId, siteId) }),
      ]),
  });
};

export const useDisconnectRepository = (projectId: string, siteId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => disconnectRepository(projectId, siteId),
    onSettled: () => qc.invalidateQueries({ queryKey: gitKey(projectId, siteId) }),
  });
};

export const useRebuildSite = (projectId: string, siteId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => rebuildSite(projectId, siteId),
    onSettled: () => qc.invalidateQueries({ queryKey: buildsKey(projectId, siteId) }),
  });
};

export const useBuildSecrets = (projectId: string, enabled = true) =>
  useQuery({
    queryKey: secretsKey(projectId),
    queryFn: () => listBuildSecrets(projectId),
    enabled: enabled && !!projectId,
  });

export const useSetBuildSecrets = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (buildVisible: string[]) => setBuildSecrets(projectId, buildVisible),
    onSuccess: (listed) => qc.setQueryData(secretsKey(projectId), listed),
  });
};
