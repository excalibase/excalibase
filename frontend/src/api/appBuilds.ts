import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import type { GitAccess, SiteBuildStatus } from './siteBuilds';

// Git builds of apps (EXC-547): a push to the tracked branch builds an image and deploys it.
export type AppBuilder = 'auto' | 'dockerfile' | 'buildpacks';

export interface AppBuildSettings {
  branch: string;
  builder: AppBuilder;
  rootDir: string;
  // Relative to the root directory; empty means Dockerfile.
  dockerfilePath: string;
  // Plain values; they end up in the image's history, so never a secret.
  buildArgs: Record<string, string>;
}

export interface AppGitConnection {
  appId: string;
  provider: string;
  repository: string;
  repositoryId: number;
  settings: AppBuildSettings;
  connectedBy: string;
  connectedByName?: string;
  // Set once the connector lost the developer rung; a developer has to connect again.
  inactiveReason?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AppGitState {
  connected: boolean;
  connection: AppGitConnection | null;
  builders: AppBuilder[];
}

export interface AppBuild {
  id: string;
  appId: string;
  status: SiteBuildStatus;
  trigger: 'push' | 'rebuild';
  commitSha: string;
  commitMessage?: string;
  repository: string;
  app?: AppBuildSettings & { maxImageBytes: number };
  deployId?: string;
  failureKind?: string;
  failureReason?: string;
  log?: string;
  requestedBy: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface AppConnectResult {
  connection: AppGitConnection;
  build?: AppBuild;
  buildError?: string;
}

export const EMPTY_APP_SETTINGS: AppBuildSettings = {
  branch: '',
  builder: 'auto',
  rootDir: '',
  dockerfilePath: '',
  buildArgs: {},
};

const base = (projectId: string, appId: string) => `/projects/${projectId}/apps/${appId}`;
const gitKey = (projectId: string, appId: string) => ['app-git', projectId, appId] as const;
const buildsKey = (projectId: string, appId: string) => ['app-builds', projectId, appId] as const;

const ACTIVE = new Set<SiteBuildStatus>(['QUEUED', 'RUNNING']);
export const isAppBuildActive = (build: AppBuild) => ACTIVE.has(build.status);

// install opens GitHub's install page for the App; otherwise its sign-in.
export const startAppGitSignIn = async (
  projectId: string,
  appId: string,
  install: boolean,
): Promise<string> =>
  (await api.post<{ url: string }>(`${base(projectId, appId)}/git/sign-in`, { install })).data.url;

export const useAppGitState = (projectId: string, appId: string, enabled: boolean) =>
  useQuery({
    queryKey: gitKey(projectId, appId),
    queryFn: async () => (await api.get<AppGitState>(`${base(projectId, appId)}/git`)).data,
    enabled: enabled && !!projectId && !!appId,
    retry: false,
  });

export const useAppGitRepositories = (projectId: string, appId: string, enabled: boolean) =>
  useQuery({
    queryKey: ['app-git-repositories', projectId, appId],
    queryFn: async () =>
      (await api.get<GitAccess[]>(`${base(projectId, appId)}/git/repositories`)).data,
    enabled: enabled && !!projectId && !!appId,
    retry: false,
  });

// Polls while a build is queued or running, so its status follows along.
export const useAppBuilds = (projectId: string, appId: string) =>
  useQuery({
    queryKey: buildsKey(projectId, appId),
    queryFn: async () => (await api.get<AppBuild[]>(`${base(projectId, appId)}/builds`)).data,
    enabled: !!projectId && !!appId,
    refetchInterval: (query) => (query.state.data?.some(isAppBuildActive) ? 5000 : false),
  });

export const useAppBuild = (projectId: string, appId: string, buildId: string) =>
  useQuery({
    queryKey: [...buildsKey(projectId, appId), buildId],
    queryFn: async () =>
      (await api.get<AppBuild>(`${base(projectId, appId)}/builds/${buildId}`)).data,
    refetchInterval: (query) =>
      query.state.data && isAppBuildActive(query.state.data) ? 5000 : false,
  });

export const useConnectAppRepository = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: { repositoryId: number; settings: AppBuildSettings }) =>
      (await api.put<AppConnectResult>(`${base(projectId, appId)}/git`, input)).data,
    onSettled: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: gitKey(projectId, appId) }),
        qc.invalidateQueries({ queryKey: buildsKey(projectId, appId) }),
      ]),
  });
};

export const useDisconnectAppRepository = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await api.delete(`${base(projectId, appId)}/git`);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: gitKey(projectId, appId) }),
  });
};

export const useRebuildApp = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.post<AppBuild>(`${base(projectId, appId)}/builds`)).data,
    onSettled: () => qc.invalidateQueries({ queryKey: buildsKey(projectId, appId) }),
  });
};
