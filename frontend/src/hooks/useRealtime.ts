import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';

export interface RealtimeTable {
  schema: string;
  table: string;
  enabled: boolean;
}

const tablesKey = (projectId: string) => ['realtime', projectId, 'tables'] as const;

/**
 * Lists every user-data table in the project's database with its current
 * publication-membership state. Polls infrequently (60s) since toggle
 * mutations invalidate the cache directly — polling is just a safety
 * net for state changes from another tab or via direct SQL.
 */
export function useRealtimeTables(projectId: string) {
  return useQuery<RealtimeTable[]>({
    queryKey: tablesKey(projectId),
    queryFn: async () => {
      const { data } = await api.get<RealtimeTable[]>(
        `/projects/${projectId}/realtime/tables`,
      );
      return data;
    },
    enabled: !!projectId,
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
}

export function useEnableRealtimeTable(projectId: string) {
  const qc = useQueryClient();
  return useMutation<RealtimeTable, Error, { schema: string; table: string }>({
    mutationFn: async ({ schema, table }) => {
      const { data } = await api.put<RealtimeTable>(
        `/projects/${projectId}/realtime/tables/${schema}/${table}`,
      );
      return data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: tablesKey(projectId) });
    },
  });
}

export function useDisableRealtimeTable(projectId: string) {
  const qc = useQueryClient();
  return useMutation<RealtimeTable, Error, { schema: string; table: string }>({
    mutationFn: async ({ schema, table }) => {
      const { data } = await api.delete<RealtimeTable>(
        `/projects/${projectId}/realtime/tables/${schema}/${table}`,
      );
      return data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: tablesKey(projectId) });
    },
  });
}

export function useEnableAllRealtime(projectId: string) {
  const qc = useQueryClient();
  return useMutation<{ added: number }, Error, void>({
    mutationFn: async () => {
      const { data } = await api.post<{ added: number }>(
        `/projects/${projectId}/realtime/enable-all`,
      );
      return data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: tablesKey(projectId) });
    },
  });
}

export function useDisableAllRealtime(projectId: string) {
  const qc = useQueryClient();
  return useMutation<{ dropped: number }, Error, void>({
    mutationFn: async () => {
      const { data } = await api.post<{ dropped: number }>(
        `/projects/${projectId}/realtime/disable-all`,
      );
      return data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: tablesKey(projectId) });
    },
  });
}
