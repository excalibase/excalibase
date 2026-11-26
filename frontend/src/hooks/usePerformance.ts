import { useQuery, useMutation } from '@tanstack/react-query';
import { api } from '../api/client';

// What GET /provision/{id}/performance/{summary,top-queries,wait-events} send.
export interface QueryStat {
  query: string;
  calls: number;
  totalExecTimeMs: number;
  avgExecTimeMs: number;
  minExecTimeMs: number;
  maxExecTimeMs: number;
  rows: number;
}

// Wait events come grouped: how many sessions wait on each event.
export interface WaitEvent {
  waitEventType: string;
  waitEvent: string;
  count: number;
}

// `available` is false, with a reason, when pg_stat_statements is off.
export interface PerformanceSummary {
  activeConnections: number | null;
  totalConnections: number | null;
  cacheHitRatio: number | null;
  databaseSize?: string;
  slowQueryCount: number | null;
  avgQueryTimeMs: number | null;
  available: boolean;
  unavailableReason?: string;
}

export const usePerformanceSummary = (projectId: string) => {
  return useQuery({
    queryKey: ['performance', 'summary', projectId],
    queryFn: async () => {
      const response = await api.get<PerformanceSummary>(`/provision/${projectId}/performance/summary`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 30000,
  });
};

export const useTopQueries = (projectId: string, limit: number = 10) => {
  return useQuery({
    queryKey: ['performance', 'top-queries', projectId, limit],
    queryFn: async () => {
      const response = await api.get<QueryStat[]>(`/provision/${projectId}/performance/top-queries?limit=${limit}`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 30000,
  });
};

export const useWaitEvents = (projectId: string) => {
  return useQuery({
    queryKey: ['performance', 'wait-events', projectId],
    queryFn: async () => {
      const response = await api.get<WaitEvent[]>(`/provision/${projectId}/performance/wait-events`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 15000,
  });
};

export const useEnablePerformanceInsights = (projectId: string) => {
  return useMutation({
    mutationFn: async () => {
      const response = await api.post<Record<string, string>>(`/provision/${projectId}/performance/enable`);
      return response.data;
    },
  });
};
