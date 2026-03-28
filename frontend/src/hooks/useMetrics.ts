import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { api } from '../api/client';
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
  const [isConnected, setIsConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId || !enabled) return;

    const baseURL = api.defaults.baseURL || 'http://localhost:24005';
    const eventSource = new EventSource(`${baseURL}/api/provision/${projectId}/metrics/stream`);

    eventSource.onopen = () => {
      setIsConnected(true);
      setError(null);
    };

    eventSource.onmessage = (event) => {
      try {
        const metrics: DatabaseMetrics = JSON.parse(event.data);
        setLatestMetrics(metrics);
      } catch (err) {
        console.error('Failed to parse metrics event:', err);
        setError('Failed to parse server event');
      }
    };

    eventSource.onerror = (err) => {
      console.error('SSE connection error:', err);
      setIsConnected(false);
      setError('Connection lost');
      eventSource.close();
    };

    return () => {
      eventSource.close();
      setIsConnected(false);
    };
  }, [projectId, enabled]);

  return { latestMetrics, isConnected, error };
};
