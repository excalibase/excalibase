import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A project's SDK api keys, as GET /api/projects/{projectId}/sdk-keys lists
// them. excalibase-auth mints and stores them; Studio sees the full key only
// in the response that created it.
export type SdkKeyType = 'publishable' | 'secret';

export interface SdkKey {
  id: number;
  keyPrefix: string;
  keyType: SdkKeyType;
  name: string;
  createdAt: string;
  lastUsedAt?: string;
}

export interface CreatedSdkKey extends SdkKey {
  plaintext: string;
}

const namespaces: Record<SdkKeyType, string> = {
  publishable: 'esk_pub_live_',
  secret: 'esk_sec_live_',
};

export function displayPrefix(key: SdkKey): string {
  return `${namespaces[key.keyType] ?? ''}${key.keyPrefix}`;
}

const keysPath = (projectId: string) => `/projects/${projectId}/sdk-keys/`;

export function useSdkKeys(projectId: string) {
  return useQuery({
    queryKey: ['sdk-keys', projectId],
    queryFn: async () => (await api.get<{ keys: SdkKey[] }>(keysPath(projectId))).data.keys,
  });
}

export function useCreateSdkKey(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; keyType: SdkKeyType }) =>
      (await api.post<CreatedSdkKey>(keysPath(projectId), body)).data,
    onSuccess: () => client.invalidateQueries({ queryKey: ['sdk-keys', projectId] }),
  });
}

export function useRevokeSdkKey(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`${keysPath(projectId)}${id}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: ['sdk-keys', projectId] }),
  });
}
