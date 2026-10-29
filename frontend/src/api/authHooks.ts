import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// End-user auth hooks (EXC-662): signed HTTPS calls the auth service makes to
// the project's own endpoints. The signing secret is answered once, on generate.
export type AuthHookName = 'custom_access_token' | 'before_user_created' | 'send_email';

export interface AuthHook {
  hook: AuthHookName;
  enabled: boolean;
  url: string;
}

export interface AuthHooks {
  secretSet: boolean;
  hooks: AuthHook[];
}

export interface AuthHookUpdate {
  enabled: boolean;
  url: string;
}

const hooksKey = (projectId: string) => ['auth-hooks', projectId] as const;
const hooksPath = (projectId: string) => `/projects/${projectId}/auth-hooks`;

export const useAuthHooks = (projectId: string) =>
  useQuery({
    queryKey: hooksKey(projectId),
    queryFn: async () => (await api.get<AuthHooks>(hooksPath(projectId))).data,
    enabled: !!projectId,
  });

export const useSaveAuthHook = (projectId: string, hook: AuthHookName) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (update: AuthHookUpdate) => (await api.put<AuthHooks>(`${hooksPath(projectId)}/${hook}`, update)).data,
    onSuccess: (saved) => queryClient.setQueryData(hooksKey(projectId), saved),
  });
};

// The answer is the only time the secret is readable; the cache keeps only that one is set.
export const useRotateAuthHookSecret = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.post<{ secret: string }>(`${hooksPath(projectId)}/secret`)).data.secret,
    gcTime: 0,
    onSuccess: () =>
      queryClient.setQueryData<AuthHooks>(hooksKey(projectId), (current) => (current ? { ...current, secretSet: true } : current)),
  });
};

export const useRemoveAuthHookSecret = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.delete<AuthHooks>(`${hooksPath(projectId)}/secret`)).data,
    onSuccess: (removed) => queryClient.setQueryData(hooksKey(projectId), removed),
  });
};
