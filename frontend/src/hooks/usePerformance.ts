import { useQuery, useMutation } from '@tanstack/react-query';
import { api } from '../api/client';

export interface QueryStat {
  query: string;
  calls: number;
  totalTimeMs: number;
  meanTimeMs: number;
  rows: number;
  hitPercent: number;
}

export interface WaitEvent {
  pid: number;
  waitEventType: string;
  waitEvent: string;
  state: string;
  query: string;
  duration: string;
}

export interface PerformanceSummary {
  projectId: string;
  cacheHitRatio: number;
  activeConnections: number;
  idleConnections: number;
  totalConnections: number;
  maxConnections: number;
  databaseSizeBytes: number;
  slowQueryCount: number;
  topQueries: QueryStat[];
  waitEvents: WaitEvent[];
  collectedAt: string;
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
