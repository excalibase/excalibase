import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import { getApp, listDeploys, type App, type Deploy } from './apps';
import type { TierType } from '../types';

// Built-in templates (EXC-526) as GET /api/projects/{projectId}/app-templates/ serves them.
export type TemplateVarSource = 'literal' | 'generated' | 'database' | 'app';

export interface TemplateVar {
  name: string;
  source: TemplateVarSource;
  // Only a plain literal carries its value; a generated one does not exist until deploy.
  value?: string;
  // Returned to the deployer once, in the deploy's answer.
  showOnce?: boolean;
}

export interface TemplateApp {
  name: string;
  image: string;
  internal: boolean;
  port?: number;
  internalPorts?: number[];
  replicas: number;
  args?: string[];
  disk?: { mountPath: string; size: string };
  minMemory?: string;
  env: TemplateVar[];
}

// What the template costs this project's plan, and why it cannot go in now.
export interface TemplateFit {
  plan: TierType;
  appsNeeded: number;
  appsHeld: number;
  appsAllowed: number;
  diskBytes: number;
  diskCapBytes: number;
  appCpu: string;
  appMemory: string;
  privateNetworkOn: boolean;
  canTurnOnPrivateNetwork: boolean;
  databaseReady: boolean;
  refusals: string[];
}

export interface AppTemplate {
  id: string;
  format: string;
  name: string;
  summary: string;
  description?: string;
  category?: TemplateCategory;
  license?: string;
  // A self-managed service of the catalog (EXC-669), not a starter.
  catalog?: boolean;
  apps: TemplateApp[];
  needsPrivateNetwork: boolean;
  needsDatabase: boolean;
  fit: TemplateFit;
  source?: string;
}

export type TemplateCategory = 'starter' | 'database' | 'cache' | 'queue' | 'search' | 'monitoring' | 'analytics';

// A generated value the deploy returns once; the platform cannot show it again.
export interface TemplateCredential {
  app: string;
  name: string;
  value: string;
}

// The deploy answers once the apps exist; their first deploys run after it (EXC-669).
export interface TemplateDeployResult {
  templateId: string;
  apps: { id: string; name: string }[];
  privateNetworkTurnedOn: boolean;
  credentials?: TemplateCredential[];
  acceptedAt: string;
}

export type FirstDeploy =
  | { state: 'starting' }
  | { state: 'rolling' }
  | { state: 'running' }
  | { state: 'failed'; reason: string };

// The operation an app records when its first deploy from a template could not start.
const TEMPLATE_DEPLOY = 'template deploy';

// How the app's first deploy stands: a failure the app recorded since the
// deploy was accepted, else its newest deploy.
export function firstDeploy(app: App, newest: Deploy | undefined, acceptedAt: string): FirstDeploy {
  const failure = app.lifecycleFailure;
  if (failure?.operation === TEMPLATE_DEPLOY && Date.parse(failure.at) >= Date.parse(acceptedAt)) {
    return { state: 'failed', reason: failure.reason };
  }
  if (!newest) return { state: 'starting' };
  if (newest.status === 'succeeded') return { state: 'running' };
  if (newest.status === 'failed') return { state: 'failed', reason: newest.failureReason ?? 'the deploy failed' };
  return { state: 'rolling' };
}

const FIRST_DEPLOY_POLL_MS = 3000;

// Reads the app and its newest deploy until the first deploy has finished.
export const useFirstDeploy = (projectId: string, appId: string, acceptedAt: string) =>
  useQuery({
    queryKey: ['apps', projectId, appId, 'first-deploy', acceptedAt],
    queryFn: async () => {
      const [app, deploys] = await Promise.all([getApp(projectId, appId), listDeploys(projectId, appId, 1)]);
      return firstDeploy(app, deploys[0], acceptedAt);
    },
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === 'running' || state === 'failed' ? false : FIRST_DEPLOY_POLL_MS;
    },
  });

const templatesKey = (projectId: string) => ['app-templates', projectId] as const;

export const useAppTemplates = (projectId: string, enabled = true) =>
  useQuery({
    queryKey: templatesKey(projectId),
    queryFn: async () =>
      (await api.get<AppTemplate[]>(`/projects/${projectId}/app-templates/`)).data,
    enabled: enabled && !!projectId,
  });

export const useAppTemplateSource = (projectId: string, templateId: string, enabled: boolean) =>
  useQuery({
    queryKey: [...templatesKey(projectId), templateId],
    queryFn: async () =>
      (await api.get<AppTemplate>(`/projects/${projectId}/app-templates/${templateId}`)).data,
    enabled: enabled && !!projectId,
  });

export const useDeployTemplate = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (args: { templateId: string; confirmPrivateNetwork: boolean }) =>
      (
        await api.post<TemplateDeployResult>(
          `/projects/${projectId}/app-templates/${args.templateId}/deploy`,
          { confirmPrivateNetwork: args.confirmPrivateNetwork },
        )
      ).data,
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['apps', projectId] });
      queryClient.invalidateQueries({ queryKey: templatesKey(projectId) });
      queryClient.invalidateQueries({ queryKey: ['app-network', projectId] });
    },
  });
};

// A refusal lists its reasons; anything else is one message.
export function templateRefusal(err: unknown): string[] {
  const data = (err as { response?: { data?: { error?: unknown; reasons?: unknown } } } | null)
    ?.response?.data;
  if (Array.isArray(data?.reasons) && data.reasons.length > 0) return data.reasons.map(String);
  if (typeof data?.error === 'string' && data.error.trim() !== '') return [data.error];
  if (!(err as { response?: unknown } | null)?.response)
    return ['The deploy could not be sent: the server could not be reached.'];
  return ['The template was not deployed.'];
}

export function formatBytes(bytes: number): string {
  const gib = 1 << 30;
  const mib = 1 << 20;
  if (bytes >= gib && bytes % gib === 0) return `${bytes / gib}Gi`;
  return `${Math.round(bytes / mib)}Mi`;
}
