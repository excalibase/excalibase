import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Metrics keys (EXC-657): read-only keys for one project's Prometheus endpoint.

export interface MetricsKey {
  // The key's hash: what revoke takes, never the key itself.
  id: string;
  name: string;
  tokenPrefix: string;
  createdAt?: string;
  expiresAt?: string;
  lastUsed?: string;
}

export interface IssuedMetricsKey extends MetricsKey {
  token: string;
}

export const METRICS_KEY_EXPIRY_DAYS = [30, 90, 180, 365] as const;
export const DEFAULT_METRICS_KEY_DAYS = 90;

const keysUrl = (projectId: string) => `/projects/${projectId}/metrics-keys`;
const keysKey = (projectId: string) => ['metrics-keys', projectId];

export const listMetricsKeys = async (projectId: string): Promise<MetricsKey[]> =>
  (await api.get<MetricsKey[]>(keysUrl(projectId))).data;

export const useMetricsKeys = (projectId: string, enabled: boolean) =>
  useQuery({
    queryKey: keysKey(projectId),
    queryFn: () => listMetricsKeys(projectId),
    enabled: enabled && !!projectId,
  });

export const useCreateMetricsKey = (projectId: string) => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (input: { name: string; expiresInDays: number }) =>
      (await api.post<IssuedMetricsKey>(keysUrl(projectId), input)).data,
    onSuccess: () => client.invalidateQueries({ queryKey: keysKey(projectId) }),
  });
};

export const useRevokeMetricsKey = (projectId: string) => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`${keysUrl(projectId)}/${id}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: keysKey(projectId) }),
  });
};
