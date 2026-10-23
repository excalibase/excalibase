import { useQuery, useMutation, useQueryClient, keepPreviousData } from '@tanstack/react-query';
import { api } from '../api/client';
import { slowQueriesPath, type SlowQueryReport, type SlowQuerySort } from '../utils/slowQueries';

export const SLOW_QUERY_ROWS = 50;

export const useSlowQueries = (projectId: string, sort: SlowQuerySort) =>
  useQuery({
    queryKey: ['slow-queries', projectId, sort],
    queryFn: async () => (await api.get<SlowQueryReport>(slowQueriesPath(projectId, sort, SLOW_QUERY_ROWS))).data,
    enabled: !!projectId,
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  });

function useSlowQueryAction(projectId: string, action: 'reset' | 'enable') {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await api.post(`/provision/${encodeURIComponent(projectId)}/slow-queries/${action}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: ['slow-queries', projectId] }),
  });
}

export const useResetSlowQueries = (projectId: string) => useSlowQueryAction(projectId, 'reset');
export const useEnableSlowQueries = (projectId: string) => useSlowQueryAction(projectId, 'enable');
