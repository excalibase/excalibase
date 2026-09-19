import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';
import { api } from '../api/client';
import { useSSE } from './useSSE';
import type { DatabaseInstance, ProvisioningRequest, CredentialsResponse, BackupConfig, BackupInfo } from '../types';

export const useInstances = () => {
  return useQuery({
    queryKey: ['instances'],
    queryFn: async () => {
      const response = await api.get<DatabaseInstance[]>('/provision');
      return response.data;
    },
    // Removed polling - using SSE for real-time updates instead
    // Keep staleTime to allow refetching on mount/window focus
    staleTime: 30000, // Consider data fresh for 30 seconds
  });
};

export const useInstance = (projectId: string) => {
  return useQuery({
    queryKey: ['instance', projectId],
    queryFn: async () => {
      const response = await api.get<DatabaseInstance>(`/provision/${projectId}`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 3000, // Poll more frequently for single instance
  });
};

/**
 * Real-time SSE hook for provisioning updates
 * Replaces polling with Server-Sent Events
 */
export const useInstanceSSE = (projectId: string) => {
  const queryClient = useQueryClient();

  const handleMessage = useCallback((instance: DatabaseInstance) => {
    // Update React Query cache with new data
    queryClient.setQueryData(['instance', projectId], instance);

    // Also update the instances list
    queryClient.setQueryData<DatabaseInstance[]>(['instances'], (old) => {
      if (!old) return [instance];
      const index = old.findIndex((i) => i.projectId === projectId);
      if (index === -1) return [...old, instance];
      const updated = [...old];
      updated[index] = instance;
      return updated;
    });
  }, [projectId, queryClient]);

  return useSSE<DatabaseInstance>(
    projectId ? `/provision/${projectId}/events` : null,
    handleMessage,
    { enabled: !!projectId },
  );
};

export const useCredentials = (projectId: string) => {
  return useQuery({
    queryKey: ['credentials', projectId],
    queryFn: async () => {
      const response = await api.get<CredentialsResponse>(`/provision/${projectId}/credentials`);
      return response.data;
    },
    enabled: !!projectId,
  });
};

export const useProvisionDatabase = () => {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (request: ProvisioningRequest) => {
      const response = await api.post<DatabaseInstance>('/provision', request);
      return response.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

export const useDeprovisionDatabase = () => {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (projectId: string) => {
      await api.delete(`/provision/${projectId}`);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

interface PauseResponse {
  projectId: string;
  status: string;
  pauseReason?: string;
}

export const usePauseProject = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ projectId, reason }: { projectId: string; reason?: string }) => {
      const response = await api.post<PauseResponse>(`/provision/${projectId}/pause`, { reason: reason ?? 'manual' });
      return response.data;
    },
    onSuccess: (_, vars) => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
      queryClient.invalidateQueries({ queryKey: ['instance', vars.projectId] });
      // SettingsPage queryKey is ['project', projectId] — invalidate that
      // too so the page repaints with the new status without a manual refresh.
      queryClient.invalidateQueries({ queryKey: ['project', vars.projectId] });
    },
  });
};

export const useResumeProject = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (projectId: string) => {
      const response = await api.post<PauseResponse>(`/provision/${projectId}/resume`);
      return response.data;
    },
    onSuccess: (_, projectId) => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
      queryClient.invalidateQueries({ queryKey: ['instance', projectId] });
      queryClient.invalidateQueries({ queryKey: ['project', projectId] });
    },
  });
};

export const useConfigureBackup = () => {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ projectId, config }: { projectId: string; config: BackupConfig }) => {
      const response = await api.post(`/provision/${projectId}/backup/configure`, config);
      return response.data;
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['instance', variables.projectId] });
    },
  });
};

export const useTriggerBackup = () => {
  return useMutation({
    mutationFn: async (projectId: string) => {
      const response = await api.post(`/provision/${projectId}/backup/trigger`);
      return response.data;
    },
  });
};

export const useListBackups = (projectId: string) => {
  return useQuery({
    queryKey: ['backups', projectId],
    queryFn: async () => {
      const response = await api.get<{ backups: BackupInfo[]; backupEnabled: boolean; schedule: string; retentionDays: number }>(
        `/provision/${projectId}/backup/list`
      );
      return response.data;
    },
    enabled: !!projectId,
  });
};

export interface RestoreRequest {
  // Display name for the restored project. The project id is generated by
  // the platform and returned on the restore job.
  newProjectName: string;
  targetTime?: string;       // ISO-8601, e.g. "2024-01-15T12:00:00" — leave blank for full restore
  backupId?: string;
  storageClassName?: string;
}

export const useRestoreFromBackup = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (request: RestoreRequest) => {
      const response = await api.post<Record<string, string>>(
        `/provision/${projectId}/backup/restore`, request
      );
      return response.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

export const useLogs = (projectId: string, lines: number = 100) => {
  return useQuery({
    queryKey: ['logs', projectId, lines],
    queryFn: async () => {
      const response = await api.get<string>(`/provision/${projectId}/logs?lines=${lines}`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 15000,
  });
};
