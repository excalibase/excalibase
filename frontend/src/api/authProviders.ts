import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// End-user Google and GitHub sign-in for the project's own app (EXC-662). The
// client secret is write-only: the server only says whether one is set.
export interface AuthProvider {
  provider: string;
  enabled: boolean;
  clientId: string;
  clientSecretSet: boolean;
  callbackUrl: string;
}

export interface AuthProviderUpdate {
  enabled: boolean;
  clientId: string;
  clientSecret?: string;
}

const providersKey = (projectId: string) => ['auth-providers', projectId] as const;
const providersPath = (projectId: string) => `/projects/${projectId}/auth-providers`;

// providerUpdateBody leaves a blank secret out, which keeps the stored one.
export function providerUpdateBody(enabled: boolean, clientId: string, secret: string): AuthProviderUpdate {
  const body: AuthProviderUpdate = { enabled, clientId: clientId.trim() };
  if (secret !== '') body.clientSecret = secret;
  return body;
}

export const useAuthProviders = (projectId: string, enabled: boolean) =>
  useQuery({
    queryKey: providersKey(projectId),
    queryFn: async () => (await api.get<{ providers: AuthProvider[] }>(providersPath(projectId))).data.providers,
    enabled: enabled && !!projectId,
  });

export const useSaveAuthProvider = (projectId: string, provider: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (update: AuthProviderUpdate) =>
      (await api.put<AuthProvider>(`${providersPath(projectId)}/${provider}`, update)).data,
    onSuccess: (saved) =>
      queryClient.setQueryData<AuthProvider[]>(providersKey(projectId), (current) =>
        current?.map((entry) => (entry.provider === saved.provider ? saved : entry))),
  });
};
