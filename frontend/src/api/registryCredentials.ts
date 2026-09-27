import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

export interface RegistryCredentialInput {
  registry: string;
  username: string;
  password: string;
}

const base = (projectId: string) => `/projects/${projectId}/registry-credentials`;
const key = (projectId: string) => ['registry-credentials', projectId] as const;

// The server names registries only; a credential is never sent back.
export const listRegistryCredentials = async (projectId: string): Promise<string[]> =>
  (await api.get<Array<{ registry: string }>>(`${base(projectId)}/`)).data.map((e) => e.registry);

export const useRegistryCredentials = (projectId: string) =>
  useQuery({
    queryKey: key(projectId),
    queryFn: () => listRegistryCredentials(projectId),
    enabled: !!projectId,
  });

export const useSetRegistryCredential = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ registry, username, password }: RegistryCredentialInput) => {
      await api.put(`${base(projectId)}/${encodeURIComponent(registry)}`, { username, password });
    },
    onSettled: () => qc.invalidateQueries({ queryKey: key(projectId) }),
  });
};

export const useRemoveRegistryCredential = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (registry: string) => {
      await api.delete(`${base(projectId)}/${encodeURIComponent(registry)}`);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: key(projectId) }),
  });
};
