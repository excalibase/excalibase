import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import type { TierType } from '../types';

// Built-in templates (EXC-526) as GET /api/projects/{projectId}/app-templates/ serves them.
export type TemplateVarSource = 'literal' | 'generated' | 'database' | 'app';

export interface TemplateVar {
  name: string;
  source: TemplateVarSource;
  // Only a plain literal carries its value; a generated one does not exist until deploy.
  value?: string;
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
  apps: TemplateApp[];
  needsPrivateNetwork: boolean;
  needsDatabase: boolean;
  fit: TemplateFit;
  source?: string;
}

export interface TemplateDeployResult {
  templateId: string;
  apps: { id: string; name: string; deployId: string; deployStatus: string }[];
  privateNetworkTurnedOn: boolean;
}

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
