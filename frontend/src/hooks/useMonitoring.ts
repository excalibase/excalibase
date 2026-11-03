import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  getAlertSettings,
  getContainerHistory,
  getHostMonitoring,
  getMonitoredContainers,
  getProjectContainerMetrics,
  saveAlertSettings,
  sendTestAlert,
  type AlertSettingsUpdate,
  type MonitoringRange,
} from '../api/monitoring';

// The sampler writes once a minute, so polling faster shows nothing new.
const POLL_MS = 60_000;

export const useHostMonitoring = (range: MonitoringRange) =>
  useQuery({
    queryKey: ['monitoring', 'host', range],
    queryFn: () => getHostMonitoring(range),
    refetchInterval: POLL_MS,
    placeholderData: keepPreviousData,
  });

export const useMonitoredContainers = () =>
  useQuery({
    queryKey: ['monitoring', 'containers'],
    queryFn: getMonitoredContainers,
    refetchInterval: POLL_MS,
  });

export const useContainerHistory = (name: string, range: MonitoringRange) =>
  useQuery({
    queryKey: ['monitoring', 'containers', name, range],
    queryFn: () => getContainerHistory(name, range),
    refetchInterval: POLL_MS,
    placeholderData: keepPreviousData,
  });

const ALERT_SETTINGS_KEY = ['monitoring', 'alert-settings'];

export const useAlertSettings = () =>
  useQuery({ queryKey: ALERT_SETTINGS_KEY, queryFn: getAlertSettings });

export const useSaveAlertSettings = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AlertSettingsUpdate) => saveAlertSettings(body),
    onSuccess: (saved) => queryClient.setQueryData(ALERT_SETTINGS_KEY, saved),
  });
};

export const useSendTestAlert = () => useMutation({ mutationFn: sendTestAlert });

export const useProjectContainerMetrics = (projectId: string, range: MonitoringRange) =>
  useQuery({
    queryKey: ['monitoring', 'project', projectId, range],
    queryFn: () => getProjectContainerMetrics(projectId, range),
    enabled: !!projectId,
    refetchInterval: POLL_MS,
    placeholderData: keepPreviousData,
  });
