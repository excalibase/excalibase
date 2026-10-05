import { useCallback } from 'react';
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
  // An internal service (EXC-525) has no HTTP port (0) and no public URL.
  internal?: boolean;
  // Raw TCP ports only this project's apps reach, with its private network on (EXC-525).
  internalPorts?: InternalPort[];
  healthCheckPath?: string;
  // Replace the image's CMD; $(NAME) is filled from the app's variables (EXC-526).
  args?: string[];
  replicas: number;
  disk?: AppDisk;
  tier: TierType;
  status: string;
  // Why the last pause, resume or deletion did not complete (EXC-523).
  lifecycleFailure?: LifecycleFailure;
  // The image watcher deploys the tag's new digest when it moves (EXC-542).
  autoDeploy?: boolean;
  imageWatch?: ImageWatch;
  // The digest the last deploy by tag resolved the image to.
  resolvedDigest?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  // Not served yet; shown as soon as the API starts returning it.
  url?: string;
}

export type LifecycleOperation = 'pause' | 'resume' | 'deletion';

export interface LifecycleFailure {
  operation: string;
  reason: string;
  at: string;
}

export interface ImageWatch {
  digest?: string;
  checkedAt: string;
  error?: string;
}

export type DeployStatus = 'pending' | 'rolling' | 'succeeded' | 'failed' | 'superseded';

// Where a deploy was asked for: Studio, the API (CI or a script), or the image watcher.
export type DeploySource = 'studio' | 'api' | 'image-watcher';

export interface Deploy {
  id: string;
  appId: string;
  projectId: string;
  revision: number;
  // What ran: the reference pinned to its digest when it was deployed by tag.
  image: string;
  // The reference as named, and the digest it resolved to.
  imageRef?: string;
  digest?: string;
  source?: DeploySource;
  commitSha?: string;
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
  internal: boolean;
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
// A pause, resume or deletion the app records as not completed.
export class LifecycleFailedError extends Error {}

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
  if (err instanceof PartialSaveError || err instanceof LifecycleFailedError) return err.message;
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

export const setAutoDeploy = async (
  projectId: string,
  appId: string,
  version: number,
  autoDeploy: boolean,
): Promise<App> =>
  (
    await api.patch<App>(`${appsBase(projectId)}/${appId}`, { autoDeploy }, {
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

// A pause, resume or deletion may wait minutes for the app's lease and pods
// (EXC-523), so the server answers once it has started it; acceptedAt tells a
// failure the app records afterwards from an older one.
export interface AppLifecycleAccepted {
  id: string;
  status: string;
  acceptedAt: string;
}

const RESPOND_ASYNC = { headers: { Prefer: 'respond-async' } };

export const pauseApp = async (projectId: string, appId: string): Promise<AppLifecycleAccepted> =>
  (await api.post<AppLifecycleAccepted>(`${appsBase(projectId)}/${appId}/pause`, undefined, RESPOND_ASYNC)).data;

export const resumeApp = async (projectId: string, appId: string): Promise<AppLifecycleAccepted> =>
  (await api.post<AppLifecycleAccepted>(`${appsBase(projectId)}/${appId}/resume`, undefined, RESPOND_ASYNC)).data;

// A container with a disk is only deleted with the explicit confirmation that erases it.
export const deleteApp = async (
  projectId: string,
  appId: string,
  confirmDeleteDisk = false,
): Promise<AppLifecycleAccepted> => {
  const url = `${appsBase(projectId)}/${appId}`;
  const response = confirmDeleteDisk
    ? await api.delete<AppLifecycleAccepted>(url, { data: { confirmDeleteDisk: true }, ...RESPOND_ASYNC })
    : await api.delete<AppLifecycleAccepted>(url, RESPOND_ASYNC);
  return response.data;
};

// The app, or null once it is gone.
const findApp = async (projectId: string, appId: string): Promise<App | null> => {
  try {
    return await getApp(projectId, appId);
  } catch (err) {
    if ((err as { response?: { status?: number } } | null)?.response?.status === 404) return null;
    throw err;
  }
};

export interface PendingLifecycle {
  operation: LifecycleOperation;
  acceptedAt: string;
}

export type LifecycleOutcome =
  | { state: 'pending' }
  | { state: 'done' }
  | { state: 'failed'; reason: string };

const LIFECYCLE_DONE: Record<LifecycleOperation, (app: App | null) => boolean> = {
  pause: (app) => app?.status === 'PAUSED',
  resume: (app) => app?.status === 'ACTIVE',
  deletion: (app) => app === null,
};

// Whether the operation started at acceptedAt has finished, from the app as read.
export function lifecycleOutcome(pending: PendingLifecycle, app: App | null): LifecycleOutcome {
  const failure = app?.lifecycleFailure;
  if (failure?.operation === pending.operation && Date.parse(failure.at) >= Date.parse(pending.acceptedAt)) {
    return { state: 'failed', reason: failure.reason };
  }
  return LIFECYCLE_DONE[pending.operation](app) ? { state: 'done' } : { state: 'pending' };
}

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

export const useSetAutoDeploy = (projectId: string, appId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ version, autoDeploy }: { version: number; autoDeploy: boolean }) =>
      setAutoDeploy(projectId, appId, version, autoDeploy),
    onSuccess: (app) => qc.setQueryData(appKey(projectId, appId), app),
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
  operation: LifecycleOperation,
  action: (projectId: string, appId: string) => Promise<AppLifecycleAccepted>,
) =>
  useMutation({
    mutationFn: async (): Promise<PendingLifecycle> => ({
      operation,
      acceptedAt: (await action(projectId, appId)).acceptedAt,
    }),
  });

export const usePauseApp = (projectId: string, appId: string) =>
  useLifecycle(projectId, appId, 'pause', pauseApp);

export const useResumeApp = (projectId: string, appId: string) =>
  useLifecycle(projectId, appId, 'resume', resumeApp);

export const useDeleteApp = (projectId: string, appId: string, confirmDeleteDisk: boolean) =>
  useLifecycle(projectId, appId, 'deletion', (project, app) => deleteApp(project, app, confirmDeleteDisk));

// Re-reads the project's containers, their deploys and disks.
export const useRefreshApps = (projectId: string) => {
  const qc = useQueryClient();
  return useCallback(() => qc.invalidateQueries({ queryKey: appsKey(projectId) }), [qc, projectId]);
};

// Reads the app until the operation it started finishes or records why it did
// not, keeping the page's copy of the app current meanwhile.
export const useFollowLifecycle = (
  projectId: string,
  appId: string,
  pending: PendingLifecycle | null,
  pollMs: number,
) => {
  const qc = useQueryClient();
  return useQuery({
    queryKey: [...appKey(projectId, appId), 'lifecycle', pending?.acceptedAt],
    queryFn: async (): Promise<LifecycleOutcome> => {
      const app = await findApp(projectId, appId);
      if (app) qc.setQueryData(appKey(projectId, appId), app);
      return pending ? lifecycleOutcome(pending, app) : { state: 'done' };
    },
    enabled: pending !== null,
    refetchInterval: (query) => (query.state.data?.state === 'pending' ? pollMs : false),
    gcTime: 0,
  });
};

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
