import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import type { TierType } from '../types';

export type VarKind = 'literal' | 'reference' | 'secret';

export const DATABASE_VARIABLES = [
  'DATABASE_URL',
  'PGHOST',
  'PGPORT',
  'PGDATABASE',
  'PGUSER',
  'PGPASSWORD',
] as const;

export interface ReferenceTarget {
  sourceKind: 'database';
  sourceName: string;
  variable: string;
}

export interface SecretRef {
  path: string;
  key: string;
}

export interface EnvVar {
  name: string;
  kind: VarKind;
  value?: string;
  reference?: ReferenceTarget;
  secret?: SecretRef;
}

// One persistent disk a container keeps across restarts, redeploys and pauses.
export interface AppDisk {
  mountPath: string;
  // Whole mebibytes or gibibytes, such as "500Mi" or "10Gi".
  size: string;
  generation?: number;
}

// A measurement of the disk. usedBytes and filesystemBytes are absent until a
// deploy has created it.
export interface AppDiskStatus {
  mountPath: string;
  size: string;
  sizeBytes: number;
  usedBytes?: number;
  filesystemBytes?: number;
  planMax: string;
  planMaxBytes: number;
  overPlan: boolean;
  measuredAt?: string;
}

export interface App {
  id: string;
  projectId: string;
  name: string;
  image: string;
  env: EnvVar[];
  port: number;
  // Raw TCP ports only this project's apps reach, with its private network on (EXC-525).
  internalPorts?: InternalPort[];
  healthCheckPath?: string;
  replicas: number;
  disk?: AppDisk;
  tier: TierType;
  status: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  // Not served yet; shown as soon as the API starts returning it.
  url?: string;
}

export type DeployStatus = 'pending' | 'rolling' | 'succeeded' | 'failed' | 'superseded';

export interface Deploy {
  id: string;
  appId: string;
  projectId: string;
  revision: number;
  image: string;
  redeployOf?: string;
  status: DeployStatus;
  failureReason?: string;
  createdBy: string;
  createdAt: string;
  finishedAt?: string;
}

export interface InternalPort {
  port: number;
  protocol: 'TCP';
}

export interface AppInput {
  name: string;
  image: string;
  port: number;
  internalPorts: InternalPort[];
  replicas: number;
  healthCheckPath: string;
  env: EnvVar[];
  disk?: AppDisk;
}

export interface SecretValue {
  name: string;
  value: string;
}

export interface AppSubmission {
  input: AppInput;
  secrets: SecretValue[];
}

// The app was written but one of its secret values was not. Carries the app so
// the caller can move on to editing it instead of creating it twice.
export class PartialSaveError extends Error {
  readonly app: App;
  readonly secretName: string;

  constructor(app: App, secretName: string, cause: unknown) {
    super(
      `The container was saved, but the secret ${secretName} was not: ${apiErrorMessage(cause, 'the server refused it')}`,
    );
    this.app = app;
    this.secretName = secretName;
  }
}

export const isDeployInProgress = (status: DeployStatus): boolean =>
  status === 'pending' || status === 'rolling';

// The API answers refusals as {"error": "..."}; anything else is a network or
// client failure and is reported as such.
export function apiErrorMessage(err: unknown, fallback: string): string {
  if (err instanceof PartialSaveError) return err.message;
  const response = (err as { response?: { data?: { error?: unknown } } } | null)?.response;
  const message = response?.data?.error;
  if (typeof message === 'string' && message.trim() !== '') return message;
  if (!response) return `${fallback}: the server could not be reached.`;
  return fallback;
}

const appsBase = (projectId: string) => `/projects/${projectId}/apps`;
const appsKey = (projectId: string) => ['apps', projectId] as const;
const appKey = (projectId: string, appId: string) => ['apps', projectId, appId] as const;
const deploysKey = (projectId: string, appId: string) =>
  ['apps', projectId, appId, 'deploys'] as const;
const diskKey = (projectId: string, appId: string) => ['apps', projectId, appId, 'disk'] as const;

export const listApps = async (projectId: string): Promise<App[]> =>
  (await api.get<App[]>(`${appsBase(projectId)}/`)).data;

export const getApp = async (projectId: string, appId: string): Promise<App> =>
  (await api.get<App>(`${appsBase(projectId)}/${appId}`)).data;

export const createApp = async (projectId: string, input: AppInput): Promise<App> =>
  (await api.post<App>(`${appsBase(projectId)}/`, input)).data;

export const updateApp = async (
  projectId: string,
  appId: string,
  version: number,
  input: AppInput,
): Promise<App> =>
  (
    await api.patch<App>(`${appsBase(projectId)}/${appId}`, input, {
      headers: { 'If-Match': String(version) },
    })
  ).data;

// Write-only: the response confirms the value is set and never carries it.
export const setAppSecret = async (
  projectId: string,
  appId: string,
  secret: SecretValue,
): Promise<void> => {
  await api.put(`${appsBase(projectId)}/${appId}/secrets/${encodeURIComponent(secret.name)}`, {
    value: secret.value,
  });
};

async function storeSecrets(projectId: string, app: App, secrets: SecretValue[]): Promise<App> {
  for (const secret of secrets) {
    try {
      await setAppSecret(projectId, app.id, secret);
    } catch (err) {
      throw new PartialSaveError(app, secret.name, err);
    }
  }
  return app;
}

export const listDeploys = async (
  projectId: string,
  appId: string,
  limit?: number,
): Promise<Deploy[]> => {
  const query = limit ? `?limit=${limit}` : '';
  const { data } = await api.get<Deploy[]>(`${appsBase(projectId)}/${appId}/deploys${query}`);
  return [...data].sort((a, b) => b.revision - a.revision);
};

export const deployApp = async (projectId: string, appId: string): Promise<Deploy> =>
  (await api.post<Deploy>(`${appsBase(projectId)}/${appId}/deploy`)).data;

export const redeployApp = async (
  projectId: string,
  appId: string,
  deployId: string,
): Promise<Deploy> =>
  (await api.post<Deploy>(`${appsBase(projectId)}/${appId}/deploys/${deployId}/redeploy`)).data;

export interface AppLifecycleResult {
  id: string;
  status: string;
}

export const pauseApp = async (projectId: string, appId: string): Promise<AppLifecycleResult> =>
  (await api.post<AppLifecycleResult>(`${appsBase(projectId)}/${appId}/pause`)).data;

export const resumeApp = async (projectId: string, appId: string): Promise<AppLifecycleResult> =>
  (await api.post<AppLifecycleResult>(`${appsBase(projectId)}/${appId}/resume`)).data;

// Answers once the container's pods are gone and it is forgotten. A container
// with a disk is only deleted with the explicit confirmation that erases it.
export const deleteApp = async (
  projectId: string,
  appId: string,
  confirmDeleteDisk = false,
): Promise<void> => {
  if (confirmDeleteDisk) {
    await api.delete(`${appsBase(projectId)}/${appId}`, { data: { confirmDeleteDisk: true } });
    return;
  }
  await api.delete(`${appsBase(projectId)}/${appId}`);
};

export const getAppDisk = async (projectId: string, appId: string): Promise<AppDiskStatus> =>
  (await api.get<AppDiskStatus>(`${appsBase(projectId)}/${appId}/disk`)).data;

// Grows, or lowers a stopped app's disk; the server holds it to the plan cap.
export const resizeAppDisk = async (
  projectId: string,
  appId: string,
  size: string,
): Promise<{ id: string; disk: AppDisk }> =>
  (await api.post<{ id: string; disk: AppDisk }>(`${appsBase(projectId)}/${appId}/disk`, { size }))
    .data;

export const useApps = (projectId: string, enabled = true) =>
  useQuery({
    queryKey: appsKey(projectId),
    queryFn: () => listApps(projectId),
    enabled: enabled && !!projectId,
  });

export const useApp = (projectId: string, appId: string) =>
  useQuery({
    queryKey: appKey(projectId, appId),
    queryFn: () => getApp(projectId, appId),
    enabled: !!projectId && !!appId,
  });

// Polls only while the newest deploy is still moving, so an idle page makes
// no requests.
export const useDeploys = (projectId: string, appId: string, pollMs: number, limit?: number) =>
  useQuery({
    queryKey: [...deploysKey(projectId, appId), limit ?? 'all'],
    queryFn: () => listDeploys(projectId, appId, limit),
    enabled: !!projectId && !!appId,
    refetchInterval: (query) => {
      const newest = query.state.data?.[0];
      return newest && isDeployInProgress(newest.status) ? pollMs : false;
    },
  });

// The app is written first so its secrets have an app to belong to.
export const useCreateApp = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ input, secrets }: AppSubmission) =>
      storeSecrets(projectId, await createApp(projectId, input), secrets),
    onSettled: () => qc.invalidateQueries({ queryKey: appsKey(projectId) }),
  });
};

export const useUpdateApp = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ version, input, secrets }: AppSubmission & { version: number }) =>
      storeSecrets(projectId, await updateApp(projectId, appId, version, input), secrets),
    onSettled: () => qc.invalidateQueries({ queryKey: appsKey(projectId) }),
  });
};

export const useDeployApp = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => deployApp(projectId, appId),
    onSuccess: () => qc.invalidateQueries({ queryKey: deploysKey(projectId, appId) }),
  });
};

export const useRedeployApp = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (deployId: string) => redeployApp(projectId, appId, deployId),
    onSuccess: () => qc.invalidateQueries({ queryKey: deploysKey(projectId, appId) }),
  });
};

const useLifecycle = (
  projectId: string,
  appId: string,
  action: (projectId: string, appId: string) => Promise<unknown>,
) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => action(projectId, appId),
    onSettled: () => qc.invalidateQueries({ queryKey: appsKey(projectId) }),
  });
};

export const usePauseApp = (projectId: string, appId: string) =>
  useLifecycle(projectId, appId, pauseApp);

export const useResumeApp = (projectId: string, appId: string) =>
  useLifecycle(projectId, appId, resumeApp);

export const useDeleteApp = (projectId: string, appId: string, confirmDeleteDisk: boolean) =>
  useLifecycle(projectId, appId, (project, app) => deleteApp(project, app, confirmDeleteDisk));

// Each read runs a probe on the cluster, so it is fetched once and on Refresh.
export const useAppDisk = (projectId: string, appId: string) =>
  useQuery({
    queryKey: diskKey(projectId, appId),
    queryFn: () => getAppDisk(projectId, appId),
    enabled: !!projectId && !!appId,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });

export const useResizeAppDisk = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (size: string) => resizeAppDisk(projectId, appId, size),
    onSettled: () => qc.invalidateQueries({ queryKey: appsKey(projectId) }),
  });
};
