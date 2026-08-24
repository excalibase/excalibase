import { useQuery } from '@tanstack/react-query';
import { useState, useCallback } from 'react';
import { api } from '../api/client';
import { useSSE } from './useSSE';
import type { DatabaseMetrics, MetricsHistory } from '../types';

/**
 * Get current metrics snapshot
 */
export const useCurrentMetrics = (projectId: string) => {
  return useQuery({
    queryKey: ['metrics', projectId, 'current'],
    queryFn: async () => {
      const response = await api.get<DatabaseMetrics>(`/provision/${projectId}/metrics/current`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 10000, // Fallback polling every 10 seconds
  });
};

/**
 * Get metrics history
 */
export const useMetricsHistory = (projectId: string, limit: number = 50) => {
  return useQuery({
    queryKey: ['metrics', projectId, 'history', limit],
    queryFn: async () => {
      const response = await api.get<MetricsHistory>(`/provision/${projectId}/metrics/history?limit=${limit}`);
      return response.data;
    },
    enabled: !!projectId,
  });
};

/**
 * Real-time metrics via SSE
 */
export const useMetricsSSE = (projectId: string, enabled: boolean = true) => {
  const [latestMetrics, setLatestMetrics] = useState<DatabaseMetrics | null>(null);

  const handleMessage = useCallback((metrics: DatabaseMetrics) => {
    setLatestMetrics(metrics);
  }, []);

  const { isConnected, error } = useSSE<DatabaseMetrics>(
    projectId ? `/provision/${projectId}/metrics/stream` : null,
    handleMessage,
    { enabled: !!projectId && enabled },
  );

  return { latestMetrics, isConnected, error };
};
