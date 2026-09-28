import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// ClusterSettings is the database as the control plane reads it off the
// cluster: disk, size, plan and the tenant's Postgres settings (EXC-492).
export interface ClusterSettings {
  projectId: string;
  tier: string;
  orgTier: string;
  storageSize: string;
  // The disk the plan starts with, and the most it may grow to.
  storageStart: string;
  storageLimit: string;
  // What the databases take on disk; null when it could not be read.
  storageUsedBytes: number | null;
  // Whether the caller may change size, plan or settings (Admin and up).
  canChange: boolean;
  instances: number;
  cpu: string;
  memory: string;
  parameters: Record<string, string>;
  tunableParameters: string[];
}

const settingsKey = (projectId: string) => ['cluster-settings', projectId];

export const useClusterSettings = (projectId: string, enabled: boolean) =>
  useQuery({
    queryKey: settingsKey(projectId),
    queryFn: async () => (await api.get<ClusterSettings>(`/provision/${projectId}/cluster`)).data,
    enabled: enabled && !!projectId,
  });

// Each change answers the settings as they now are; the cache takes that
// answer rather than an outcome Studio assumed.
function useClusterChange<T>(projectId: string, send: (body: T) => Promise<ClusterSettings>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: send,
    onSuccess: (settings) => {
      queryClient.setQueryData(settingsKey(projectId), settings);
      queryClient.invalidateQueries({ queryKey: ['instance', projectId] });
    },
  });
}

export const useResizeStorage = (projectId: string) =>
  useClusterChange<string>(
    projectId,
    async (size) =>
      (await api.post<ClusterSettings>(`/provision/${projectId}/storage`, { size })).data,
  );

export const useChangeTier = (projectId: string) =>
  useClusterChange<string>(
    projectId,
    async (tier) =>
      (await api.post<ClusterSettings>(`/provision/${projectId}/tier`, { tier })).data,
  );

export const useTuneParameters = (projectId: string) =>
  useClusterChange<Record<string, string>>(
    projectId,
    async (parameters) =>
      (await api.put<ClusterSettings>(`/provision/${projectId}/parameters`, { parameters })).data,
  );

// refusalMessage is the control plane's own reason for a refused change.
export function refusalMessage(error: unknown): string {
  const response = (error as { response?: { data?: { error?: string } } })?.response;
  if (response?.data?.error) return response.data.error;
  return error instanceof Error ? error.message : String(error);
}
