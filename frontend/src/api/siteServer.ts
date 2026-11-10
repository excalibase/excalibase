import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A server site's plain runtime variables and the secrets its code may read (EXC-661, ADR 0045).
export interface ServerSecret {
  readonly name: string;
  readonly serverVisible: boolean;
}

const envKey = (projectId: string, siteId: string) => ['site-env', projectId, siteId] as const;
const secretsKey = (projectId: string) => ['site-server-secrets', projectId] as const;
const envUrl = (projectId: string, siteId: string) => `/projects/${projectId}/sites/${siteId}/env`;
const secretsUrl = (projectId: string) => `/projects/${projectId}/sites/server-secrets`;

export const useSiteEnv = (projectId: string, siteId: string) =>
  useQuery({
    queryKey: envKey(projectId, siteId),
    queryFn: async () =>
      (await api.get<{ vars: Record<string, string> }>(envUrl(projectId, siteId))).data.vars,
    enabled: !!projectId && !!siteId,
    retry: false,
  });

export const useSetSiteEnv = (projectId: string, siteId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (vars: Record<string, string>) =>
      (await api.put<{ vars: Record<string, string> }>(envUrl(projectId, siteId), { vars })).data
        .vars ?? vars,
    onSuccess: (_saved, vars) => qc.setQueryData(envKey(projectId, siteId), vars),
  });
};

export const useServerSecrets = (projectId: string) =>
  useQuery({
    queryKey: secretsKey(projectId),
    queryFn: async () => (await api.get<ServerSecret[]>(secretsUrl(projectId))).data,
    enabled: !!projectId,
    retry: false,
  });

export const useSetServerSecrets = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (serverVisible: string[]) =>
      (await api.put<ServerSecret[]>(secretsUrl(projectId), { serverVisible })).data,
    onSuccess: (listed, serverVisible) =>
      qc.setQueryData(
        secretsKey(projectId),
        Array.isArray(listed) && listed.length > 0
          ? listed
          : (qc.getQueryData<ServerSecret[]>(secretsKey(projectId)) ?? []).map((secret) => ({
              ...secret,
              serverVisible: serverVisible.includes(secret.name),
            })),
      ),
  });
};
